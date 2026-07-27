package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolErrorClass buckets a single tool call's outcome into a small taxonomy
// used to decide whether a run is worth a human's review. The key insight is
// that a tool *failure* is not, by itself, a useful signal — models routinely
// fail a call and retry it correctly. Only a couple of classes correlate with
// the failure mode we care about (the model misusing its own toolset and then
// hallucinating), and even those only matter when they go unrecovered. See
// EvaluateReview for how the classes combine into a verdict.
//
// This mirrors the pure-classifier shape of ClassifyError in classify_error.go.
type ToolErrorClass string

const (
	// ToolErrorNone — the call succeeded (or produced no error signal). A
	// later success of the same tool is what clears an earlier failure, so
	// successes are recorded too, not just failures.
	ToolErrorNone ToolErrorClass = ""

	// ToolErrorUnknownTool — the model invoked a tool that does not exist.
	// High-signal: this is itself a hallucination artifact.
	ToolErrorUnknownTool ToolErrorClass = "unknown_tool"

	// ToolErrorInvalidArgs — the model produced arguments that don't satisfy
	// the tool's schema (malformed JSON / wrong shape). High-signal, and a
	// sustained run of these (thrash) is a strong confusion signal even when a
	// later attempt eventually succeeds.
	ToolErrorInvalidArgs ToolErrorClass = "invalid_args"

	// ToolErrorExecutionError — the tool ran but failed (bad path, non-zero
	// exit, domain error). Weak signal: real work legitimately hits these and
	// the model usually handles or correctly reports them. Never flags alone.
	ToolErrorExecutionError ToolErrorClass = "execution_error"

	// ToolErrorTransient — a stream/network error of the kind the agent already
	// retries internally. Excluded from flagging entirely as noise.
	ToolErrorTransient ToolErrorClass = "transient"

	// ToolErrorSuppressed — an intentional dedupe/skip emitted by the agent
	// loop (e.g. a duplicate terminal tool call), not a model error. Skipped
	// by the evaluator.
	ToolErrorSuppressed ToolErrorClass = "suppressed"
)

// HighSignal reports whether this class is one of the two that correlate with
// tool-misuse hallucination: inventing a tool, or failing to satisfy a schema.
func (c ToolErrorClass) HighSignal() bool {
	return c == ToolErrorUnknownTool || c == ToolErrorInvalidArgs
}

// Classify normalizes coding-agent text sentinels and AI-chat JSON soft errors.
// Execution errors count like success for misuse flagging; intentional skips are
// excluded. It is pure and safe without locking.
func Classify(toolName, output string) ToolErrorClass {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return ToolErrorNone
	}

	if strings.HasPrefix(trimmed, "(unknown tool:") {
		return ToolErrorUnknownTool
	}
	if strings.HasPrefix(trimmed, "(tool arguments were not valid JSON:") {
		return ToolErrorInvalidArgs
	}

	if strings.HasPrefix(trimmed, "{") {
		var body struct {
			Error   string `json:"error"`
			Skipped bool   `json:"skipped"`
		}
		if err := json.Unmarshal([]byte(trimmed), &body); err == nil {
			if body.Skipped {
				return ToolErrorSuppressed
			}
			if body.Error != "" {
				msg := strings.ToLower(body.Error)
				switch {
				case isTransientWording(msg):
					return ToolErrorTransient
				case strings.Contains(msg, "unknown tool"):
					return ToolErrorUnknownTool
				case strings.Contains(msg, "invalid argument"):
					return ToolErrorInvalidArgs
				default:
					return ToolErrorExecutionError
				}
			}
		}
	}

	return ToolErrorNone
}

// isTransientWording matches network/stream error idioms that are already
// retried upstream, so the evaluator excludes them. Deliberately uses
// network-specific phrases (not a bare "timeout", which would collide with a
// bash command-timeout that is a real execution error).
func isTransientWording(lower string) bool {
	for _, phrase := range []string{
		"connection refused",
		"connection reset",
		"no such host",
		"i/o timeout",
		"context deadline exceeded",
		"stream error",
		"tls handshake",
		"eof\n", // body that is just an EOF line; avoids matching words ending in "eof"
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// ToolCallOutcome is one classified tool call in the order it occurred within a
// single run/exchange. The Tool name is the recovery key: a high-signal failure
// is considered recovered only by a later success of the same tool.
type ToolCallOutcome struct {
	Tool  string
	Class ToolErrorClass
}

// ReviewFlagConfig tunes the flag rule.
type ReviewFlagConfig struct {
	// InvalidArgsThrashN — flag when a single tool accumulates this many
	// invalid_args attempts in one run, even if a later attempt recovered.
	// Sustained schema-thrash is itself a confusion signal. Default 3.
	InvalidArgsThrashN int
}

// DefaultReviewFlagConfig returns the tuned defaults.
func DefaultReviewFlagConfig() ReviewFlagConfig {
	return ReviewFlagConfig{InvalidArgsThrashN: 3}
}

// ReviewVerdict is the per-run result. Flagged means "high-signal, unrecovered
// tool misuse — worth a human glance"; it is deliberately NOT a claim that the
// run hallucinated.
type ReviewVerdict struct {
	Flagged            bool
	Reasons            []string
	UnrecoveredClasses []ToolErrorClass
}

// EvaluateReview walks the ordered tool calls of one run and decides whether to
// flag it. The recovery rule is the load-bearing false-positive guard: a
// high-signal failure at index i is *recovered* iff some later call (j > i) of
// the same tool succeeded (ToolErrorNone). Recovered failures do not flag.
//
// A run is flagged if ANY of:
//  1. an unrecovered unknown_tool call,
//  2. an unrecovered invalid_args call,
//  3. invalid_args thrash on a single tool (>= InvalidArgsThrashN attempts),
//     even if a later attempt recovered.
//
// execution_error and transient never flag on their own; suppressed calls are
// ignored entirely.
func EvaluateReview(calls []ToolCallOutcome, cfg ReviewFlagConfig) ReviewVerdict {
	if cfg.InvalidArgsThrashN <= 0 {
		cfg.InvalidArgsThrashN = DefaultReviewFlagConfig().InvalidArgsThrashN
	}

	var verdict ReviewVerdict

	recovered := func(i int) bool {
		for j := i + 1; j < len(calls); j++ {
			if calls[j].Tool == calls[i].Tool && calls[j].Class == ToolErrorNone {
				return true
			}
		}
		return false
	}

	// Count invalid_args per tool in first-appearance order, for deterministic
	// thrash reasons.
	thrash := map[string]int{}
	var toolOrder []string
	seenTool := map[string]bool{}
	for _, c := range calls {
		if !seenTool[c.Tool] {
			seenTool[c.Tool] = true
			toolOrder = append(toolOrder, c.Tool)
		}
		if c.Class == ToolErrorInvalidArgs {
			thrash[c.Tool]++
		}
	}

	var unrecoveredUnknown, unrecoveredInvalid bool
	for i, c := range calls {
		if !c.Class.HighSignal() || recovered(i) {
			continue
		}
		switch c.Class {
		case ToolErrorUnknownTool:
			unrecoveredUnknown = true
		case ToolErrorInvalidArgs:
			unrecoveredInvalid = true
		}
	}

	if unrecoveredUnknown {
		verdict.UnrecoveredClasses = append(verdict.UnrecoveredClasses, ToolErrorUnknownTool)
		verdict.Reasons = append(verdict.Reasons,
			"unrecovered unknown_tool call (model invoked a tool that does not exist)")
	}
	if unrecoveredInvalid {
		verdict.UnrecoveredClasses = append(verdict.UnrecoveredClasses, ToolErrorInvalidArgs)
		verdict.Reasons = append(verdict.Reasons,
			"unrecovered invalid_args call (model could not satisfy the tool's argument schema)")
	}
	for _, tool := range toolOrder {
		if thrash[tool] >= cfg.InvalidArgsThrashN {
			verdict.Reasons = append(verdict.Reasons,
				fmt.Sprintf("invalid_args thrash on %q (%d attempts)", tool, thrash[tool]))
		}
	}

	verdict.Flagged = len(verdict.Reasons) > 0
	return verdict
}
