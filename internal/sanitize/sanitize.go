// Package sanitize is the single home for input-sanitization policies
// across the app. Every service that accepts user-supplied text should
// route through these intent-named policies; the goal is that picking
// the right policy is the obvious thing — wrong choices read wrong.
//
// Policies are named by *what kind of field they sanitize*, not by the
// underlying primitive. PlainTextField (titles, labels) and RichText
// (descriptions, notes) have very different shapes, so collapsing them
// would lose the contract; what we centralize is the policy library
// itself so a new entity-handling service doesn't reinvent the bundle.
//
// Every policy is stateless and safe for concurrent use.
package sanitize

import (
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
type Pair struct {
	Target *string
	Policy Policy
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

// PlainTextField — short, single-line user-facing label or title:
// item / asset / milestone / workspace / page / label names + titles.
// Strips every HTML tag (any HTML here is an injection attempt), trims
// surrounding whitespace, and caps at 200 runes (the standard
// length-budget for these fields across the app).
var PlainTextField Policy = PolicyFunc(plainTextField)

// ShortIdentifier — short identifier-like value (asset_tag, slug, code,
// link-type name). Same shape as PlainTextField with a 100-rune cap to
// match the tighter db column limits these fields tend to have.
var ShortIdentifier Policy = PolicyFunc(shortIdentifier)

// RichText — medium-length multi-line body content: asset / item /
// workspace / team descriptions, test-step actual results, test-step
// notes. Strips HTML except <br /> (Milkdown uses this to preserve
// blank lines on round-trip), decodes HTML entities back to plain
// text, neutralizes dangerous Markdown URL schemes (javascript:,
// vbscript:, data:), caps at 10 KiB.
var RichText Policy = PolicyFunc(richText)

// LongDocument — long-form Markdown document (workspace knowledge
// pages, runbooks). Same policy shape as RichText with a 256 KiB cap
// — page content is meaningfully larger than item descriptions, so
// the tighter cap would clip legitimate content.
var LongDocument Policy = PolicyFunc(longDocument)

// Comment — user-submitted comment content (Markdown editor input).
// Strips every HTML tag + neutralizes dangerous Markdown URLs. No
// length cap; the comments table bounds the column size at the schema
// layer.
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
	// Dangerous URL schemes in Markdown links: [text](javascript:...)
	// or ![alt](data:...). Matches both link and image syntax,
	// case-insensitive. The URL body alternates between a single-level
	// paren group `\([^)]*\)` and any non-paren character to swallow
	// payloads like `javascript:alert(1)` without stopping at the
	// inner `)` and leaving the markdown link's closing `)` as
	// residue. Two levels of nesting won't fully match — that's fine,
	// the outer `)` still terminates the match and the cleaner
	// replaces what it found.
	dangerousMarkdownURLRegex = regexp.MustCompile(`(?i)(!?\[[^\]]*\])\(\s*(javascript|vbscript|data)\s*:(?:\([^)]*\)|[^)])*\)`)
)

// stripAndCap is the common path for PlainTextField + ShortIdentifier:
// strip every HTML tag, decode entities so we don't store
// double-encoded text, trim whitespace, length-cap by rune count.
func stripAndCap(input string, maxRunes int) string {
	if input == "" {
		return input
	}
	s := html.UnescapeString(strictPolicy.Sanitize(input))
	s = strings.TrimSpace(s)
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	return s
}

func plainTextField(s string) string  { return stripAndCap(s, 200) }
func shortIdentifier(s string) string { return stripAndCap(s, 100) }

// brAllowAndCap is the common path for RichText + LongDocument: strip
// HTML except <br />, decode entities, normalize the bluemonday <br/>
// output back to <br /> for Milkdown compatibility, neutralize
// dangerous URL schemes, byte-cap.
func brAllowAndCap(input string, maxBytes int) string {
	if input == "" || input == "null" {
		return ""
	}
	s := brOnlyPolicy.Sanitize(input)
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "<br/>", "<br />")
	s = markdownURLOnly(s)
	if maxBytes > 0 && len(s) > maxBytes {
		s = s[:maxBytes]
	}
	return s
}

func richText(s string) string     { return brAllowAndCap(s, 10*1024) }
func longDocument(s string) string { return brAllowAndCap(s, 256*1024) }

func commentPolicy(s string) string {
	if s == "" {
		return ""
	}
	return markdownURLOnly(html.UnescapeString(strictPolicy.Sanitize(s)))
}

func markdownURLOnly(s string) string {
	if s == "" {
		return ""
	}
	return dangerousMarkdownURLRegex.ReplaceAllString(s, "${1}(#unsafe-link-removed)")
}
