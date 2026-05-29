package services

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"windshift/internal/models"
)

// buildFakePi compiles internal/services/testdata/fakepi into a per-test
// temp directory and returns the absolute path. The tests then point
// PiRunner.Command at the resulting binary. Per-test compilation keeps
// the tests hermetic; the binary itself takes ~200ms to build and is
// cached by the Go build cache between runs.
func buildFakePi(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fakepi")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fakepi/")
	cmd.Dir = "."
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakepi: %v\n%s", err, combined)
	}
	return out
}

func collectEvents(emit *[]string) EventSink {
	var mu sync.Mutex
	return func(eventType, payloadJSON string) error {
		mu.Lock()
		defer mu.Unlock()
		*emit = append(*emit, eventType+"|"+payloadJSON)
		return nil
	}
}

func TestPiRunner_HappyPath(t *testing.T) {
	bin := buildFakePi(t)
	r := &PiRunner{
		Command:       bin,
		Env:           map[string]string{"FAKEPI_MODE": "happy"},
		InitialPrompt: "do the thing",
		ShutdownGrace: 2 * time.Second,
	}
	var events []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := r.Run(ctx, RunInput{RunID: 1}, collectEvents(&events))
	if result.Status != models.AgentRunStatusSucceeded {
		t.Fatalf("status: want succeeded, got %q (err=%q)", result.Status, result.Error)
	}
	// Expect tool_call, tool_result, session_idle on stdout.
	wantTypes := []string{"tool_call", "tool_result", "session_idle"}
	idx := 0
	for _, ev := range events {
		if !strings.HasPrefix(ev, "stdout|") {
			continue
		}
		for _, t := range wantTypes[idx:] {
			if strings.Contains(ev, `"type":"`+t+`"`) {
				idx++
				break
			}
		}
	}
	if idx != len(wantTypes) {
		t.Errorf("expected to observe %v in order; matched %d. events=%v", wantTypes, idx, events)
	}
}

func TestPiRunner_CancelMidStreamMapsToCanceled(t *testing.T) {
	bin := buildFakePi(t)
	r := &PiRunner{
		Command:       bin,
		Env:           map[string]string{"FAKEPI_MODE": "hang"},
		InitialPrompt: "spin",
		ShutdownGrace: 500 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var events []string
	result := r.Run(ctx, RunInput{RunID: 2}, collectEvents(&events))
	if result.Status != models.AgentRunStatusCanceled {
		t.Fatalf("status: want canceled, got %q (err=%q)", result.Status, result.Error)
	}
}

func TestPiRunner_NonZeroExitMapsToFailed(t *testing.T) {
	bin := buildFakePi(t)
	r := &PiRunner{
		Command:       bin,
		Env:           map[string]string{"FAKEPI_MODE": "crash"},
		InitialPrompt: "kaboom",
		ShutdownGrace: 2 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []string
	result := r.Run(ctx, RunInput{RunID: 3}, collectEvents(&events))
	if result.Status != models.AgentRunStatusFailed {
		t.Fatalf("status: want failed, got %q (err=%q)", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, "code 7") {
		t.Errorf("expected exit code 7 in error message, got %q", result.Error)
	}
}

func TestPiRunner_NonJSONLineGetsWrapped(t *testing.T) {
	bin := buildFakePi(t)
	r := &PiRunner{
		Command:       bin,
		Env:           map[string]string{"FAKEPI_MODE": "badlines"},
		InitialPrompt: "with garbage",
		ShutdownGrace: 2 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []string
	result := r.Run(ctx, RunInput{RunID: 4}, collectEvents(&events))
	if result.Status != models.AgentRunStatusSucceeded {
		t.Fatalf("status: want succeeded, got %q", result.Status)
	}
	wrapped := false
	for _, ev := range events {
		if strings.Contains(ev, `"line":"not-json"`) {
			wrapped = true
			break
		}
	}
	if !wrapped {
		t.Errorf("expected non-JSON line to be wrapped as {line:...}; events=%v", events)
	}
}

func TestPiRunner_MissingCommandFailsFast(t *testing.T) {
	r := &PiRunner{InitialPrompt: "anything"}
	result := r.Run(context.Background(), RunInput{}, func(string, string) error { return nil })
	if result.Status != models.AgentRunStatusFailed {
		t.Errorf("want failed, got %q", result.Status)
	}
}

func TestPiRunner_MissingPromptFailsFast(t *testing.T) {
	r := &PiRunner{Command: "/bin/true"}
	result := r.Run(context.Background(), RunInput{}, func(string, string) error { return nil })
	if result.Status != models.AgentRunStatusFailed {
		t.Errorf("want failed, got %q", result.Status)
	}
}
