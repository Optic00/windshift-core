// Package sanitize provides shared, stateless input policies.
//
// Policies are selected by field type. Their length caps are the primary
// request-size defense: plain text is 256 runes, identifiers 100, and long
// text 256 KiB.
package sanitize

import (
	"crypto/sha256"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
)

// Policy is the input-sanitization contract. Implementations must be
// stateless and safe for concurrent use; the package's exported policies
// are package-level singletons.
type Policy interface {
	Sanitize(input string) string
}

// PolicyFunc adapts a plain function into a Policy.
type PolicyFunc func(string) string

// Sanitize implements Policy.
func (f PolicyFunc) Sanitize(input string) string { return f(input) }

// Apply runs the policy in-place on a string pointer. Convenience for
// the common "sanitize this struct field before persisting" pattern;
// no-op when target is nil.
func Apply(target *string, policy Policy) {
	if target == nil {
		return
	}
	*target = policy.Sanitize(*target)
}

// Pair binds a target pointer with the policy that should clean it.
// Use with ApplyAll to express a service's input policy declaratively.
//
// Label is the user-facing name for the field ("Title", "Description",
// "Group name"). It's optional — ApplyAll ignores it entirely;
// ApplyAllWithWarnings uses it to build the user-friendly warning
// strings handlers can stamp on a response.warnings field. Empty
// label means no warning is produced even if the value was modified.
type Pair struct {
	Target *string
	Policy Policy
	Label  string
}

// ApplyAll runs each (target, policy) pair in order. The canonical
// "sanitize this entity's text fields" call site:
//
//	sanitize.ApplyAll(
//	    sanitize.Pair{Target: &req.Title, Policy: sanitize.PlainTextField},
//	    sanitize.Pair{Target: &req.Description, Policy: sanitize.RichText},
//	    sanitize.Pair{Target: &req.Tag, Policy: sanitize.ShortIdentifier},
//	)
func ApplyAll(pairs ...Pair) {
	for _, p := range pairs {
		Apply(p.Target, p.Policy)
	}
}

// ApplyAllWithWarnings runs each policy and returns user-friendly
// warning strings for every field that was modified AND has a
// non-empty Label. Returned warnings are safe to drop directly into a
// response.warnings field so the frontend toast machinery can surface
// them at info severity. Empty slice when nothing changed.
//
// Callers that don't yet want warning surfacing should keep using
// ApplyAll — the value semantics are identical, ApplyAll just throws
// the warning information away.
func ApplyAllWithWarnings(pairs ...Pair) []string {
	var warnings []string
	for _, p := range pairs {
		if p.Target == nil {
			continue
		}
		before := *p.Target
		*p.Target = p.Policy.Sanitize(before)
		if p.Label == "" || *p.Target == before {
			continue
		}
		warnings = append(warnings, describeMutation(before, *p.Target, p.Label))
	}
	return warnings
}

// describeMutation returns a user-facing truncation/HTML-removal warning.
// Marker-based HTML detection is heuristic and falls back to generic copy.
func describeMutation(before, after, label string) string {
	beforeRunes := utf8.RuneCountInString(before)
	afterRunes := utf8.RuneCountInString(after)
	truncated := afterRunes < beforeRunes
	hadHTMLMarkers := strings.ContainsAny(before, "<>&")
	switch {
	case truncated && hadHTMLMarkers:
		return fmt.Sprintf("%s had HTML formatting removed and was shortened to %d characters.", label, afterRunes)
	case truncated:
		return fmt.Sprintf("%s was shortened to %d characters to fit the maximum length.", label, afterRunes)
	case hadHTMLMarkers:
		return fmt.Sprintf("%s had HTML formatting removed.", label)
	default:
		return fmt.Sprintf("%s was cleaned up for safe storage.", label)
	}
}

// PlainTextField — short, single-line user-facing label or title:
// item / asset / milestone / workspace / page / label names + titles.
// Strips every HTML tag (any HTML here is an injection attempt), trims
// surrounding whitespace, caps at 256 runes (titles surface across
// board cards, breadcrumbs, picker chips, browser tabs — pathological
// lengths break layout).
var PlainTextField Policy = PolicyFunc(plainTextField)

// ShortIdentifier — short identifier-like value (asset_tag, slug, code,
// link-type name). Same shape as PlainTextField with a 100-rune cap;
// intentionally tighter because these fields are identifier-shaped
// (e.g. "LAP-001", URL slugs) rather than free-form titles.
var ShortIdentifier Policy = PolicyFunc(shortIdentifier)

// RichText — multi-line body content (descriptions, notes, test-step
// actual results). Strips HTML except <br /> (Milkdown uses this to
// preserve blank lines on round-trip), preserves safe CommonMark
// autolinks, decodes HTML entities back to plain text, neutralizes
// dangerous Markdown URL schemes
// (javascript:, vbscript:, data:), caps at 256 KiB. Fenced Markdown
// code blocks are preserved verbatim so documentation can show HTML
// snippets without the sanitizer eating them.
var RichText Policy = PolicyFunc(richText)

// LongDocument — long-form Markdown document (workspace knowledge
// pages, runbooks). Same policy shape as RichText with a 1 MiB cap.
var LongDocument Policy = PolicyFunc(longDocument)

// Comment — user-submitted comment content (Markdown editor input).
// Strips every HTML tag, preserves safe CommonMark autolinks, and
// neutralizes dangerous Markdown URLs.
// Caps at 256 KiB, matching other non-document long-form text.
var Comment Policy = PolicyFunc(commentPolicy)

// MarkdownURLOnly neutralizes dangerous URL schemes in Markdown
// link / image syntax without touching anything else. Most callers
// should reach for RichText / LongDocument / Comment instead — those
// fold this in. Use this directly only when the input is already
// HTML-stripped upstream and you just need the URL-scheme guard.
var MarkdownURLOnly Policy = PolicyFunc(markdownURLOnly)

// --- internals ---

var (
	strictPolicy = bluemonday.StrictPolicy()
	brOnlyPolicy = func() *bluemonday.Policy {
		p := bluemonday.StrictPolicy()
		p.AllowElements("br")
		return p
	}()
	// Match dangerous Markdown link/image schemes, including single-level
	// parentheses in payloads such as javascript:alert(1).
	dangerousMarkdownURLRegex = regexp.MustCompile(`(?i)(!?\[[^\]]*\])\(\s*(javascript|vbscript|data)\s*:(?:\([^)]*\)|[^)])*\)`)
	// Milkdown serializes linked URLs as CommonMark autolinks. Protect only
	// schemes the frontend permits; otherwise the HTML sanitizer mistakes
	// the angle-bracket syntax for an HTML element and drops the link.
	safeMarkdownAutolinkRegex = regexp.MustCompile("(?i)<(?:(?:https?://|mailto:|tel:|page:)[^\\x00-\\x20<>]*|[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9.-]+)>")
)

// stripAndCap is the common path for PlainTextField + ShortIdentifier:
// decode entities, strip every HTML tag, preserve safe decoded prose,
// trim whitespace, length-cap by rune count.
func stripAndCap(input string, maxRunes int) string {
	if input == "" {
		return input
	}
	s := sanitizeDecoded(input, strictPolicy)
	s = strings.TrimSpace(s)
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}

// Length caps — see the package doc + per-policy comments for rationale.
const (
	// PlainTextFieldMaxRunes bounds titles + names. 256 runes keeps
	// pathological lengths from breaking board cards / breadcrumbs /
	// picker chips that render these fields verbatim.
	PlainTextFieldMaxRunes = 256
	// ShortIdentifierMaxRunes bounds identifier-shaped values
	// (asset_tag, slug, link-type name). Tighter on purpose — these
	// aren't free-form titles.
	ShortIdentifierMaxRunes = 100
	// LongTextMaxBytes bounds descriptions, comments, and other non-document
	// long-form user text.
	LongTextMaxBytes = 256 * 1024
	// LongDocumentMaxBytes bounds page and runbook Markdown.
	LongDocumentMaxBytes = 1 * 1024 * 1024
)

func plainTextField(s string) string  { return stripAndCap(s, PlainTextFieldMaxRunes) }
func shortIdentifier(s string) string { return stripAndCap(s, ShortIdentifierMaxRunes) }

// brAllowAndCap is the common path for RichText + LongDocument:
// decode entities, strip HTML except <br />, normalize bluemonday's
// break output back to <br /> for Milkdown compatibility, neutralize
// dangerous URL schemes, byte-cap. Fenced Markdown code blocks are
// temporarily replaced with placeholders so literal examples like
// ```html\n<script>...\n``` survive storage. Renderers must still escape code
// spans/blocks when producing HTML.
func brAllowAndCap(input string, maxBytes int) string {
	if input == "" || input == "null" {
		return ""
	}
	s, codeBlocks := extractFencedCodeBlocks(input)
	s, autolinks := extractSafeMarkdownAutolinks(s)
	s = sanitizeDecoded(s, brOnlyPolicy)
	s = strings.ReplaceAll(s, "<br/>", "<br />")
	s = strings.ReplaceAll(s, "<br>", "<br />")
	s = markdownURLOnly(s)
	s = restoreProtectedMarkdown(s, autolinks)
	s = restoreFencedCodeBlocks(s, codeBlocks)
	if maxBytes > 0 && len(s) > maxBytes {
		s = s[:maxBytes]
	}
	return s
}

func richText(s string) string     { return brAllowAndCap(s, LongTextMaxBytes) }
func longDocument(s string) string { return brAllowAndCap(s, LongDocumentMaxBytes) }

func commentPolicy(s string) string {
	if s == "" {
		return ""
	}
	out, codeBlocks := extractFencedCodeBlocks(s)
	out, autolinks := extractSafeMarkdownAutolinks(out)
	out = sanitizeDecoded(out, strictPolicy)
	out = markdownURLOnly(out)
	out = restoreProtectedMarkdown(out, autolinks)
	out = restoreFencedCodeBlocks(out, codeBlocks)
	if len(out) > LongTextMaxBytes {
		out = out[:LongTextMaxBytes]
	}
	return out
}

const codeBlockPlaceholderPrefix = "%%WINDSHIFT_CODE_BLOCK_"

const autolinkPlaceholderPrefix = "%%WINDSHIFT_AUTOLINK_"

type protectedMarkdown struct {
	placeholder string
	content     string
}

func extractSafeMarkdownAutolinks(input string) (string, []protectedMarkdown) {
	if input == "" {
		return input, nil
	}

	matches := safeMarkdownAutolinkRegex.FindAllStringIndex(input, -1)
	if len(matches) == 0 {
		return input, nil
	}

	var out strings.Builder
	protected := make([]protectedMarkdown, 0, len(matches))
	last := 0
	for index, match := range matches {
		content := input[match[0]:match[1]]
		placeholder := markdownPlaceholder(autolinkPlaceholderPrefix, content, index, input)
		protected = append(protected, protectedMarkdown{placeholder: placeholder, content: content})
		out.WriteString(input[last:match[0]])
		out.WriteString(placeholder)
		last = match[1]
	}
	out.WriteString(input[last:])
	return out.String(), protected
}

func restoreProtectedMarkdown(s string, protected []protectedMarkdown) string {
	for _, value := range protected {
		s = strings.ReplaceAll(s, value.placeholder, value.content)
	}
	return s
}

type fencedCodeBlock struct {
	placeholder string
	content     string
}

func extractFencedCodeBlocks(input string) (string, []fencedCodeBlock) {
	if input == "" {
		return input, nil
	}

	lines := strings.SplitAfter(input, "\n")
	var out strings.Builder
	var blocks []fencedCodeBlock
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		marker, count, ok := openingFence(line)
		if !ok {
			out.WriteString(line)
			continue
		}

		var block strings.Builder
		block.WriteString(line)
		closed := false
		for i++; i < len(lines); i++ {
			block.WriteString(lines[i])
			if closingFence(lines[i], marker, count) {
				closed = true
				break
			}
		}
		if !closed {
			out.WriteString(block.String())
			break
		}

		blockContent := block.String()
		placeholder := codeBlockPlaceholder(blockContent, len(blocks), input)
		blocks = append(blocks, fencedCodeBlock{placeholder: placeholder, content: blockContent})
		out.WriteString(placeholder)
	}

	return out.String(), blocks
}

func codeBlockPlaceholder(content string, index int, original string) string {
	return markdownPlaceholder(codeBlockPlaceholderPrefix, content, index, original)
}

func markdownPlaceholder(prefix, content string, index int, original string) string {
	sum := sha256.Sum256([]byte(content))
	base := fmt.Sprintf("%s%d_%x%%", prefix, index, sum[:8])
	placeholder := base
	for suffix := 1; strings.Contains(original, placeholder); suffix++ {
		placeholder = fmt.Sprintf("%s_%d", base, suffix)
	}
	return placeholder
}

func openingFence(line string) (marker byte, count int, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return 0, 0, false
	}
	marker = trimmed[0]
	if marker != '`' && marker != '~' {
		return 0, 0, false
	}
	for count < len(trimmed) && trimmed[count] == marker {
		count++
	}
	if count < 3 {
		return 0, 0, false
	}
	return marker, count, true
}

func closingFence(line string, marker byte, openingCount int) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < openingCount {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != marker {
			return false
		}
	}
	return true
}

func restoreFencedCodeBlocks(s string, blocks []fencedCodeBlock) string {
	for _, block := range blocks {
		s = strings.ReplaceAll(s, block.placeholder, block.content)
	}
	return s
}

// sanitizeDecoded fully decodes HTML entities before sanitizing so nested
// entity payloads (for example "&amp;lt;img ...&amp;gt;") cannot survive as
// encoded markup that a later decode could reanimate. After sanitizing, it
// decodes only when doing so is stable under the same policy; that preserves
// legit prose like "5 < 6 > 4" without turning escaped tags back into HTML.
func sanitizeDecoded(input string, policy *bluemonday.Policy) string {
	s := unescapeRepeated(input)
	s = policy.Sanitize(s)

	decoded := html.UnescapeString(s)
	if policy.Sanitize(decoded) == s {
		return decoded
	}
	return s
}

func unescapeRepeated(s string) string {
	for i := 0; i < 8; i++ {
		u := html.UnescapeString(s)
		if u == s {
			return s
		}
		s = u
	}
	return s
}

func markdownURLOnly(s string) string {
	if s == "" {
		return ""
	}
	return dangerousMarkdownURLRegex.ReplaceAllString(s, "${1}(#unsafe-link-removed)")
}
