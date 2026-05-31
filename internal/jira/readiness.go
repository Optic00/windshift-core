package jira

// readiness.go classifies how faithfully a Jira instance migrates into
// Windshift. It is a pure layer on top of field_mapper.go — no I/O — so the
// handler can feed it sampled issue data and the test suite can exercise the
// rules directly. The same clean/lossy/blocked taxonomy backs the
// "Migrating Jira to Windshift" whitepaper, so the two never drift apart.

// Severity classifies how faithfully a Jira concept survives import.
type Severity string

const (
	// SeverityClean: imported 1:1 with full fidelity.
	SeverityClean Severity = "clean"
	// SeverityLossy: imported but degraded — partially dropped, flattened, or
	// deferred to a later import phase.
	SeverityLossy Severity = "lossy"
	// SeverityBlocked: no mapping exists; the data is skipped entirely.
	SeverityBlocked Severity = "blocked"
)

// Finding is a single migration-fidelity observation about one Jira concept.
// UsageCount is how many sampled issues actually exercise it; 0 means the
// finding is schema-only (the concept is defined but unused in the sample).
type Finding struct {
	Entity     string   `json:"entity"`
	Category   string   `json:"category"`
	Severity   Severity `json:"severity"`
	JiraType   string   `json:"jira_type,omitempty"`
	Reason     string   `json:"reason"`
	UsageCount int      `json:"usage_count"`
}

// fieldTypeSeverity maps a resolved Windshift field type to its migration
// fidelity. user/users land cleanly today (the importer resolves them in
// Phase 0); recognized value types are deferred to Phase 1 so their *values*
// are not yet written (lossy); asset-backed and unmapped fields have no home
// in Windshift at all (blocked).
func fieldTypeSeverity(t WindshiftFieldType) (severity Severity, reason string) {
	switch t {
	case FieldTypeUser, FieldTypeUsers:
		return SeverityClean, "User-valued field; resolved to Windshift users by account/email."
	case FieldTypeUnmapped:
		return SeverityBlocked, "No Windshift equivalent for this field type; it is skipped."
	case FieldTypeAsset:
		return SeverityBlocked, "Backed by Jira Assets/Insight, which is not imported."
	default:
		// text, textarea, number, select, multiselect, date, milestone, iteration
		return SeverityLossy, "Field type is recognized and the definition maps, but custom-field values are not yet written (Phase 1), so the data is dropped for now."
	}
}

// ClassifyField turns a field-mapping suggestion (from SuggestFieldMappings)
// plus its observed usage into a Finding.
func ClassifyField(s FieldMappingSuggestion, usageCount int) Finding {
	sev, reason := fieldTypeSeverity(s.WindshiftFieldType)
	if s.Notes != "" {
		reason += " " + s.Notes
	}
	return Finding{
		Entity:     "Custom field: " + s.JiraFieldName,
		Category:   "custom_field",
		Severity:   sev,
		JiraType:   s.JiraFieldType,
		Reason:     reason,
		UsageCount: usageCount,
	}
}

// supportedADFNodes are the ADF node types the importer's ADF→Markdown
// converter (ConvertADFToMarkdownWithUsers in field_mapper.go) renders with
// full fidelity. Anything outside this set is flattened to its text content,
// losing the original structure/formatting — so its presence is a lossy
// signal. Keep this in sync with convertADFNodeWithResolver's switch.
var supportedADFNodes = map[string]bool{
	"doc":         true,
	"paragraph":   true,
	"heading":     true,
	"bulletList":  true,
	"orderedList": true,
	"listItem":    true,
	"codeBlock":   true,
	"blockquote":  true,
	"rule":        true,
	"text":        true,
	"hardBreak":   true,
	"mention":     true,
}

// ScanADF walks an ADF document (the shape Jira returns for description and
// comment bodies) and returns a histogram of node `type` → occurrence count.
// A nil or plain-string body yields an empty map.
func ScanADF(adf interface{}) map[string]int {
	counts := make(map[string]int)
	var walk func(node interface{})
	walk = func(node interface{}) {
		m, ok := node.(map[string]interface{})
		if !ok {
			return
		}
		if t, _ := m["type"].(string); t != "" {
			counts[t]++
		}
		if content, ok := m["content"].([]interface{}); ok {
			for _, c := range content {
				walk(c)
			}
		}
	}
	walk(adf)
	return counts
}

// UnsupportedADFNodes returns the subset of a ScanADF histogram whose node
// types the importer cannot render with full fidelity, with their counts.
func UnsupportedADFNodes(counts map[string]int) map[string]int {
	out := make(map[string]int)
	for t, n := range counts {
		if !supportedADFNodes[t] {
			out[t] = n
		}
	}
	return out
}

// severityWeight is the fraction of fidelity each severity retains.
func severityWeight(s Severity) float64 {
	switch s {
	case SeverityClean:
		return 1.0
	case SeverityLossy:
		return 0.5
	default: // blocked
		return 0.0
	}
}

// ScoreFindings produces a 0–100 readiness score and a per-severity tally of
// distinct findings. Each finding is weighted by its usage count (floored at 1
// so schema-only findings still register) so a lossy field touched by one
// issue barely dents the score while one touched by thousands dominates. An
// empty finding set scores 100 — nothing to lose.
func ScoreFindings(findings []Finding) (score int, bySeverity map[Severity]int) {
	bySeverity = map[Severity]int{SeverityClean: 0, SeverityLossy: 0, SeverityBlocked: 0}
	var weighted, total float64
	for _, f := range findings {
		bySeverity[f.Severity]++
		w := float64(f.UsageCount)
		if w < 1 {
			w = 1
		}
		total += w
		weighted += w * severityWeight(f.Severity)
	}
	if total == 0 {
		return 100, bySeverity
	}
	return int((weighted/total)*100 + 0.5), bySeverity
}
