package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"windshift/internal/database"
	"windshift/internal/jira"
	"windshift/internal/sanitize"
	"windshift/internal/xray"
)

type xrayImportPlan struct {
	keysByProject map[string]map[string]struct{}
	cloud         xray.DefinitionClient
	dataCenter    jira.XrayTestStepReader
	total         int
}

func prepareXrayImport(
	ctx context.Context,
	client jira.Client,
	options XrayImportOptions,
	projectKeys []string,
	openOnly bool,
) (*xrayImportPlan, error) {
	if !options.ImportTests {
		return nil, nil
	}
	lister, ok := client.(jira.XrayTestKeyLister)
	if !ok {
		return nil, fmt.Errorf("connected Jira client does not support Xray discovery")
	}

	plan := &xrayImportPlan{keysByProject: make(map[string]map[string]struct{}, len(projectKeys))}
	var issueTypeIDs []string
	if detector, isCloud := client.(jira.XrayTestIssueTypeDetector); isCloud {
		var allIssueTypes []jira.JiraIssueType
		for _, projectKey := range projectKeys {
			issueTypes, err := client.GetProjectIssueTypes(ctx, projectKey)
			if err != nil {
				return nil, fmt.Errorf("load issue types for Xray project %s: %w", projectKey, err)
			}
			allIssueTypes = append(allIssueTypes, issueTypes...)
		}
		detected, err := detector.DetectXrayTestIssueTypes(ctx, allIssueTypes)
		if err != nil {
			return nil, fmt.Errorf("verify Xray Test issue types: %w", err)
		}
		if len(detected) == 0 {
			return nil, fmt.Errorf("the selected projects no longer expose an Xray-owned Test issue type")
		}
		issueTypeIDs = detected

		cloud, err := xray.NewCloudClient(xray.CloudConfig{
			ClientID:     options.ClientID,
			ClientSecret: options.ClientSecret,
			Region:       options.Region,
		})
		if err != nil {
			return nil, err
		}
		if err := cloud.Validate(ctx); err != nil {
			return nil, err
		}
		plan.cloud = cloud
	} else {
		reader, ok := client.(jira.XrayTestStepReader)
		if !ok {
			return nil, fmt.Errorf("connected Jira Data Center client cannot read Xray Test steps")
		}
		plan.dataCenter = reader
	}

	for _, projectKey := range projectKeys {
		keys, err := lister.ListXrayTestKeys(ctx, projectKey, issueTypeIDs, openOnly)
		if err != nil {
			return nil, fmt.Errorf("list Xray Tests for project %s: %w", projectKey, err)
		}
		projectKeysSet := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			if normalized := strings.TrimSpace(key); normalized != "" {
				projectKeysSet[normalized] = struct{}{}
			}
		}
		plan.keysByProject[projectKey] = projectKeysSet
		plan.total += len(projectKeysSet)
	}
	if plan.total == 0 {
		return nil, fmt.Errorf("no Xray Tests remain in the selected project scope")
	}
	return plan, nil
}

func (p *xrayImportPlan) isTest(projectKey, issueKey string) bool {
	if p == nil {
		return false
	}
	_, ok := p.keysByProject[projectKey][issueKey]
	return ok
}

func (p *xrayImportPlan) definitions(
	ctx context.Context,
	projectKey string,
	issues []jira.JiraIssue,
) (definitions map[string]xray.Test, failures map[string]error) {
	definitions = make(map[string]xray.Test)
	failures = make(map[string]error)
	if p == nil {
		return definitions, failures
	}

	var tests []jira.JiraIssue
	for _, issue := range issues {
		if p.isTest(projectKey, issue.Key) {
			tests = append(tests, issue)
		}
	}
	if len(tests) == 0 {
		return definitions, failures
	}

	if p.cloud != nil {
		issueIDs := make([]string, 0, len(tests))
		for _, issue := range tests {
			issueIDs = append(issueIDs, issue.ID)
		}
		loaded, err := p.cloud.GetTests(ctx, issueIDs)
		if err != nil {
			for _, issue := range tests {
				failures[issue.Key] = err
			}
			return definitions, failures
		}
		for _, definition := range loaded {
			definitions[definition.IssueID] = definition
		}
		for _, issue := range tests {
			if _, exists := definitions[issue.ID]; !exists {
				failures[issue.Key] = fmt.Errorf("xray Cloud returned no definition for Jira issue %s", issue.Key)
			}
		}
		return definitions, failures
	}

	for _, issue := range tests {
		steps, err := p.dataCenter.GetXrayTestSteps(ctx, issue.Key)
		if err != nil {
			failures[issue.Key] = err
			continue
		}
		definitions[issue.ID] = xray.Test{
			IssueID:      issue.ID,
			TestTypeName: "Manual",
			Steps:        steps,
		}
	}
	return definitions, failures
}

// importXrayTestCase writes a Test and all of its manual steps atomically. A
// Jira issue routed here must not also be passed to importIssue.
func (h *JiraImportHandler) importXrayTestCase(
	jobID string,
	workspaceID int,
	issue *jira.JiraIssue,
	definition xray.Test,
) (int, error) {
	if issue == nil || strings.TrimSpace(issue.ID) == "" {
		return 0, fmt.Errorf("xray Test is missing its Jira issue ID")
	}
	title := sanitize.PlainTextField.Sanitize(issue.Fields.Summary)
	if title == "" {
		title = sanitize.PlainTextField.Sanitize(issue.Key)
	}
	if title == "" {
		return 0, fmt.Errorf("xray Test %s has no title", issue.ID)
	}

	createdAt := parseJiraImportTime(issue.Fields.Created)
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := parseJiraImportTime(issue.Fields.Updated)
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	metadataJSON, err := json.Marshal(map[string]any{
		"was_created":    true,
		"xray_test_type": definition.TestTypeName,
	})
	if err != nil {
		return 0, fmt.Errorf("encode Xray Test mapping metadata: %w", err)
	}

	return database.WithTxResult(h.db, func(tx database.Tx) (int, error) {
		var maxSortOrder sql.NullInt64
		if err := tx.QueryRow(`
			SELECT MAX(sort_order)
			FROM test_cases
			WHERE workspace_id = ? AND folder_id IS NULL
		`, workspaceID).Scan(&maxSortOrder); err != nil {
			return 0, fmt.Errorf("load Xray Test sort order: %w", err)
		}

		var testCaseID int
		if err := tx.QueryRow(`
			INSERT INTO test_cases
				(workspace_id, folder_id, title, preconditions, priority, status,
				 estimated_duration, sort_order, created_at, updated_at)
			VALUES (?, NULL, ?, '', ?, 'active', 0, ?, ?, ?)
			RETURNING id
		`, workspaceID, title, jiraTestCasePriority(issue.Fields.Priority),
			int(maxSortOrder.Int64)+1000, createdAt, updatedAt).Scan(&testCaseID); err != nil {
			return 0, fmt.Errorf("create Xray Test case %s: %w", issue.Key, err)
		}

		for index, source := range definition.Steps {
			action := sanitize.Comment.Sanitize(source.Action)
			expected := sanitize.Comment.Sanitize(source.Expected)
			if _, err := tx.Exec(`
				INSERT INTO test_steps
					(test_case_id, step_number, action, data, expected, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
			`, testCaseID, index+1, action, sanitize.Comment.Sanitize(source.Data),
				expected, createdAt, updatedAt); err != nil {
				return 0, fmt.Errorf("create Xray Test step %d for %s: %w", index+1, issue.Key, err)
			}
		}

		if err := importXrayTestLabels(tx, workspaceID, testCaseID, issue.Fields.Labels, createdAt); err != nil {
			return 0, err
		}

		if _, err := tx.Exec(`
			INSERT INTO jira_import_id_mappings
				(job_id, entity_type, jira_id, jira_key, windshift_id, metadata_json)
			VALUES (?, 'test_case', ?, ?, ?, ?)
			ON CONFLICT (job_id, entity_type, jira_id) DO UPDATE SET
				windshift_id = excluded.windshift_id,
				jira_key = excluded.jira_key,
				metadata_json = excluded.metadata_json
		`, jobID, issue.ID, issue.Key, testCaseID,
			string(metadataJSON)); err != nil {
			return 0, fmt.Errorf("record Xray Test mapping for %s: %w", issue.Key, err)
		}

		return testCaseID, nil
	})
}

func importXrayTestLabels(
	tx database.Tx,
	workspaceID, testCaseID int,
	labels []string,
	createdAt time.Time,
) error {
	seen := make(map[string]struct{}, len(labels))
	for _, source := range labels {
		name := sanitize.PlainTextField.Sanitize(source)
		normalized := strings.ToLower(strings.TrimSpace(name))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}

		var labelID int
		err := tx.QueryRow(`
			SELECT id FROM test_labels WHERE workspace_id = ? AND LOWER(name) = LOWER(?)
		`, workspaceID, name).Scan(&labelID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("find Xray Test label %q: %w", name, err)
		}
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRow(`
				INSERT INTO test_labels
					(workspace_id, name, color, description, created_at, updated_at)
				VALUES (?, ?, '#3B82F6', '', ?, ?)
				RETURNING id
			`, workspaceID, name, createdAt, createdAt).Scan(&labelID); err != nil {
				return fmt.Errorf("create Xray Test label %q: %w", name, err)
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO test_case_labels (test_case_id, label_id, created_at)
			VALUES (?, ?, ?)
			ON CONFLICT (test_case_id, label_id) DO NOTHING
		`, testCaseID, labelID, createdAt); err != nil {
			return fmt.Errorf("attach Xray Test label %q: %w", name, err)
		}
	}
	return nil
}

func jiraTestCasePriority(priority *jira.JiraPriority) string {
	if priority == nil {
		return "medium"
	}
	switch strings.ToLower(strings.TrimSpace(priority.Name)) {
	case "highest", "blocker", "critical":
		return "critical"
	case "high", "major":
		return "high"
	case "low", "minor":
		return "low"
	default:
		return "medium"
	}
}

func parseJiraImportTime(value string) time.Time {
	if strings.TrimSpace(value) != "" {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-0700"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}
