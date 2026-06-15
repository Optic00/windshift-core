package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"windshift/internal/jira"
)

const (
	defaultReadinessSample = 200
	maxReadinessSample     = 500
	readinessPageSize      = 100 // legacy JQL search page cap
)

// Readiness handles POST /api/admin/jira-import/readiness. It deep-scans a
// sample of each selected project's issues and returns a migration-readiness
// report: every Jira concept classified clean / lossy / blocked, with a
// usage-weighted 0–100 score per project and overall. Unlike Analyze (which
// returns raw mapping *suggestions* for the wizard), this answers "how cleanly
// will my instance actually migrate?" before the user commits.
func (h *JiraImportHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[JiraReadinessRequest](w, r)
	if !ok {
		return
	}
	if req.ConnectionID == "" || len(req.ProjectKeys) == 0 {
		respondValidationError(w, r, "connection_id and project_keys are required")
		return
	}

	client, err := h.getClientForConnection(r.Context(), req.ConnectionID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	report := h.analyzeReadiness(r.Context(), client, req)
	respondJSONOK(w, report)
}

// analyzeReadiness is the I/O-driving core, split out so tests can inject a
// fake jira.Client (the same seam executeImportWithClient uses).
func (h *JiraImportHandler) analyzeReadiness(ctx context.Context, client jira.Client, req JiraReadinessRequest) JiraReadinessReport {
	sampleSize := req.SampleSize
	if sampleSize <= 0 {
		sampleSize = defaultReadinessSample
	}
	if sampleSize > maxReadinessSample {
		sampleSize = maxReadinessSample
	}

	report := JiraReadinessReport{
		Projects:       make([]JiraProjectReadiness, 0, len(req.ProjectKeys)),
		OpenIssuesOnly: req.OpenIssuesOnly,
	}

	// Custom-field definitions are instance-wide; fetch suggestions once and
	// index them by field ID ("customfield_10001") for per-issue usage lookup.
	fieldSuggestions := h.fieldSuggestionIndex(ctx, client, req.ProjectKeys)

	var allFindings []jira.Finding
	for _, projectKey := range req.ProjectKeys {
		pr, attachmentBytes := h.scanProject(ctx, client, projectKey, req.OpenIssuesOnly, sampleSize, fieldSuggestions)
		report.Projects = append(report.Projects, pr)
		report.TotalIssues += pr.TotalIssues
		report.SampledIssues += pr.SampledIssues
		report.AttachmentBytes += attachmentBytes
		if pr.SampledIssues < pr.TotalIssues {
			report.Extrapolated = true
		}
		allFindings = append(allFindings, pr.Findings...)
	}

	report.OverallScore, report.FindingsBySev = jira.ScoreFindings(allFindings)
	return report
}

// fieldSuggestionIndex fetches custom-field definitions (company-managed
// projects first, falling back to the all-fields endpoint) and returns them
// keyed by Jira field ID. Mirrors the field-fetch path in Analyze.
func (h *JiraImportHandler) fieldSuggestionIndex(ctx context.Context, client jira.Client, projectKeys []string) map[string]jira.FieldMappingSuggestion {
	projectIDs := make([]string, 0, len(projectKeys))
	for _, key := range projectKeys {
		project, err := client.GetProject(ctx, key)
		if err != nil || project == nil {
			continue
		}
		// Team-managed/next-gen projects don't support the field-screen API.
		if !project.Simplified && project.Style != "next-gen" {
			projectIDs = append(projectIDs, project.ID)
		}
	}

	fields, err := client.GetProjectFields(ctx, projectIDs)
	if err != nil {
		slog.Debug("readiness: GetProjectFields failed, falling back to ListCustomFields",
			slog.String("component", "jira"), slog.Any("error", err))
		fields, err = client.ListCustomFields(ctx)
		if err != nil {
			slog.Warn("readiness: could not list custom fields",
				slog.String("component", "jira"), slog.Any("error", err))
			return map[string]jira.FieldMappingSuggestion{}
		}
	}

	index := make(map[string]jira.FieldMappingSuggestion)
	for _, s := range jira.SuggestFieldMappings(fields) {
		index[s.JiraFieldID] = s
	}
	return index
}

// projectScanTally accumulates per-project counts while walking sampled issues.
type projectScanTally struct {
	sampled           int
	comments          int
	attachments       int
	attachmentBytes   int64
	labeledIssues     int
	components        int
	affectsVersions   int
	worklogs          int
	changelogs        int
	sprintIssues      int
	unsupportedADF    map[string]int  // node type -> occurrences
	customFieldUsage  map[string]int  // field ID -> issues using it
	unmappedFieldUse  int             // values seen for fields with no known mapping
	linkTypeUsage     map[string]int  // link type name -> link count
	usersMissingEmail map[string]bool // accountIDs lacking an email in the sample
}

func newProjectScanTally() *projectScanTally {
	return &projectScanTally{
		unsupportedADF:    make(map[string]int),
		customFieldUsage:  make(map[string]int),
		linkTypeUsage:     make(map[string]int),
		usersMissingEmail: make(map[string]bool),
	}
}

// scanProject samples one project's issues, tallies fidelity signals, and
// turns them into findings + a score.
func (h *JiraImportHandler) scanProject(ctx context.Context, client jira.Client, projectKey string, openOnly bool, sampleSize int, fields map[string]jira.FieldMappingSuggestion) (readiness JiraProjectReadiness, attachmentBytes int64) {
	pr := JiraProjectReadiness{Key: projectKey, Name: projectKey}

	if project, err := client.GetProject(ctx, projectKey); err == nil && project != nil {
		pr.Name = project.Name
	}

	if total, err := client.GetIssueCount(ctx, projectKey, openOnly); err == nil {
		pr.TotalIssues = total
	}

	hasSprints := false
	if boards, err := client.ListBoards(ctx, projectKey); err == nil && boards != nil && len(boards.Values) > 0 {
		hasSprints = true
	}

	issues := h.sampleIssues(ctx, client, projectKey, openOnly, sampleSize)
	tally := newProjectScanTally()
	for i := range issues {
		h.tallyIssue(&issues[i], fields, tally)
	}
	pr.SampledIssues = tally.sampled

	pr.Findings = buildFindings(tally, fields, hasSprints)
	pr.Score, _ = jira.ScoreFindings(pr.Findings)
	return pr, tally.attachmentBytes
}

// sampleIssues pages through a project's issues (newest first) up to limit,
// requesting all fields plus the changelog so the tally can see history,
// comment bodies, and custom-field values.
func (h *JiraImportHandler) sampleIssues(ctx context.Context, client jira.Client, projectKey string, openOnly bool, limit int) []jira.JiraIssue {
	jql := fmt.Sprintf("project = %s ORDER BY created DESC", projectKey)
	if openOnly {
		jql = fmt.Sprintf("project = %s AND statusCategory != Done ORDER BY created DESC", projectKey)
	}

	var out []jira.JiraIssue
	for startAt := 0; startAt < limit; startAt += readinessPageSize {
		pageSize := readinessPageSize
		if remaining := limit - startAt; remaining < pageSize {
			pageSize = remaining
		}
		res, err := client.SearchIssues(ctx, jira.SearchOptions{
			JQL:        jql,
			StartAt:    startAt,
			MaxResults: pageSize,
			Fields:     []string{"*all"},
			Expand:     []string{"changelog"},
		})
		if err != nil {
			slog.Warn("readiness: issue sample failed",
				slog.String("component", "jira"), slog.String("project", projectKey), slog.Any("error", err))
			break
		}
		if res == nil || len(res.Issues) == 0 {
			break
		}
		out = append(out, res.Issues...)
		if len(res.Issues) < pageSize {
			break // last page
		}
	}
	return out
}

// tallyIssue folds one sampled issue's fidelity signals into the running tally.
func (h *JiraImportHandler) tallyIssue(issue *jira.JiraIssue, fields map[string]jira.FieldMappingSuggestion, t *projectScanTally) {
	t.sampled++
	f := &issue.Fields

	// Rich formatting in the description.
	for node, n := range jira.UnsupportedADFNodes(jira.ScanADF(f.Description)) {
		t.unsupportedADF[node] += n
	}

	// Comments (clean) + their formatting.
	if f.Comment != nil {
		for ci := range f.Comment.Comments {
			t.comments++
			for node, n := range jira.UnsupportedADFNodes(jira.ScanADF(f.Comment.Comments[ci].Body)) {
				t.unsupportedADF[node] += n
			}
		}
	}

	t.attachments += len(f.Attachment)
	for _, att := range f.Attachment {
		t.attachmentBytes += att.Size
	}

	if len(f.Labels) > 0 {
		t.labeledIssues++
	}
	if len(f.Components) > 0 {
		t.components++
	}
	if len(f.Versions) > 0 {
		t.affectsVersions++
	}
	if (f.Worklog != nil && f.Worklog.Total > 0) || (f.TimeTracking != nil && f.TimeTracking.TimeSpentSeconds > 0) {
		t.worklogs++
	}
	if issue.Changelog != nil && issue.Changelog.Total > 0 {
		t.changelogs++
	}
	if f.Sprint != nil {
		t.sprintIssues++
	}

	for _, link := range f.IssueLinks {
		if link.Type != nil {
			t.linkTypeUsage[link.Type.Name]++
		}
	}

	for _, u := range []*jira.JiraUser{f.Assignee, f.Reporter, f.Creator} {
		if u != nil && u.EmailAddress == "" {
			if id := u.GetIdentifier(); id != "" {
				t.usersMissingEmail[id] = true
			}
		}
	}

	for fieldID, val := range f.CustomFields {
		if val == nil {
			continue
		}
		if _, known := fields[fieldID]; known {
			t.customFieldUsage[fieldID]++
		} else {
			t.unmappedFieldUse++
		}
	}
}

// buildFindings converts a completed tally into the per-project finding list.
func buildFindings(t *projectScanTally, fields map[string]jira.FieldMappingSuggestion, hasSprints bool) []jira.Finding {
	findings := make([]jira.Finding, 0, 16)

	// Clean core: the bulk of every issue (summary, description text, status,
	// type, priority, dates, assignee/reporter/creator) imports 1:1. Weighting
	// this by the sample size anchors the score against the lossy/blocked tail.
	if t.sampled > 0 {
		findings = append(findings, jira.Finding{
			Entity:     "Core issue fields",
			Category:   "core",
			Severity:   jira.SeverityClean,
			Reason:     "Summary, description text, status (by category), issue type, priority, due/created/updated dates, and assignee/reporter/creator import 1:1.",
			UsageCount: t.sampled,
		})
	}
	if t.comments > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Comments", Category: "comments", Severity: jira.SeverityClean,
			Reason: "Comment bodies import with @mention resolution and original timestamps.", UsageCount: t.comments,
		})
	}
	if t.attachments > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Attachments", Category: "attachments", Severity: jira.SeverityClean,
			Reason: "Files download and re-attach when attachment storage is configured on the Windshift side.", UsageCount: t.attachments,
		})
	}
	if t.labeledIssues > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Labels", Category: "labels", Severity: jira.SeverityClean,
			Reason: "Labels import as workspace-scoped labels.", UsageCount: t.labeledIssues,
		})
	}
	for name, n := range t.linkTypeUsage {
		findings = append(findings, jira.Finding{
			Entity: "Issue links: " + name, Category: "links", Severity: jira.SeverityClean,
			Reason: "Link type and direction are preserved for links between imported issues; links to issues outside the selected projects are dropped.", UsageCount: n,
		})
	}

	// Custom fields actually used in the sample.
	for fieldID, n := range t.customFieldUsage {
		findings = append(findings, jira.ClassifyField(fields[fieldID], n))
	}
	if t.unmappedFieldUse > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Unmapped custom fields", Category: "custom_field", Severity: jira.SeverityBlocked,
			Reason: "Values belong to custom fields with no known Windshift mapping (typically third-party/app fields) and are not imported.", UsageCount: t.unmappedFieldUse,
		})
	}

	// Lossy: rich formatting flattened to text.
	for node, n := range t.unsupportedADF {
		findings = append(findings, jira.Finding{
			Entity: "Rich formatting: " + node, Category: "formatting", Severity: jira.SeverityLossy,
			Reason: "This ADF node type has no Markdown equivalent in the importer and is flattened to its text content.", UsageCount: n,
		})
	}
	if hasSprints {
		weight := t.sprintIssues
		if weight == 0 {
			weight = 1
		}
		findings = append(findings, jira.Finding{
			Entity: "Sprints / iterations", Category: "iteration", Severity: jira.SeverityClean,
			Reason: "Boards/sprints import as Windshift iterations (name, start/end dates, state) and each issue's sprint membership is assigned to the imported item.", UsageCount: weight,
		})
	}
	if len(t.usersMissingEmail) > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Users without a visible email", Category: "users", Severity: jira.SeverityLossy,
			Reason: "Some assignees/reporters expose no email (e.g. Cloud GDPR settings); the importer creates an inactive user with a deterministic synthetic address, so the identity is preserved but the real email is not.", UsageCount: len(t.usersMissingEmail),
		})
	}

	// Lossy: imported, but as metadata or under conditions rather than as a
	// first-class Windshift concept.
	if t.components > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Components", Category: "components", Severity: jira.SeverityLossy,
			Reason: "Jira components have no first-class Windshift equivalent; they are preserved as read-only metadata on the item, not as editable components.", UsageCount: t.components,
		})
	}
	if t.affectsVersions > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Affects versions", Category: "version", Severity: jira.SeverityLossy,
			Reason: "Only fixVersions map to milestones; affects-versions are preserved as item metadata (and an optional 'Jira Affects Version/s' custom field) rather than as milestones.", UsageCount: t.affectsVersions,
		})
	}
	if t.worklogs > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Worklogs / time tracking", Category: "worklog", Severity: jira.SeverityLossy,
			Reason: "Worklog entries import into Windshift time tracking when the import maps a time project; without one they are skipped, and only the worklogs returned in the issue payload are imported, so very long histories may be truncated. Estimates are kept as item metadata.", UsageCount: t.worklogs,
		})
	}

	// Blocked: data with no Windshift home today.
	if t.changelogs > 0 {
		findings = append(findings, jira.Finding{
			Entity: "Issue history / changelog", Category: "changelog", Severity: jira.SeverityBlocked,
			Reason: "Field-change history is not imported; items start with a fresh Windshift history.", UsageCount: t.changelogs,
		})
	}

	return findings
}
