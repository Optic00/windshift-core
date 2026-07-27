package handlers

import (
	"context"
	"log/slog"
	"sort"

	"windshift/internal/jira"
)

func analyzeXrayTests(
	ctx context.Context,
	client jira.Client,
	issueTypesByProject map[string][]jira.JiraIssueType,
	openOnly bool,
) JiraXrayAnalysis {
	result := JiraXrayAnalysis{
		DetectionStatus: "not_detected",
		Projects:        make([]JiraXrayProjectAnalysis, 0),
	}

	lister, canList := client.(jira.XrayTestKeyLister)
	if !canList {
		result.DetectionStatus = "unavailable"
		result.WarningCode = "XRAY_DETECTION_UNSUPPORTED"
		return result
	}

	var issueTypeIDs []string
	if detector, isCloud := client.(jira.XrayTestIssueTypeDetector); isCloud {
		result.RequiresCredential = true
		allIssueTypes := uniqueJiraIssueTypes(issueTypesByProject)
		detected, err := detector.DetectXrayTestIssueTypes(ctx, allIssueTypes)
		if err != nil {
			slog.Debug("Xray Cloud issue-type detection unavailable",
				slog.String("component", "jira"),
				slog.Any("error", err))
			result.DetectionStatus = "unavailable"
			result.WarningCode = "XRAY_ISSUE_TYPE_PROPERTIES_UNAVAILABLE"
			return result
		}
		if len(detected) == 0 {
			return result
		}
		issueTypeIDs = detected
		result.TestIssueTypeIDs = append([]string(nil), detected...)
	}

	projectKeys := make([]string, 0, len(issueTypesByProject))
	for projectKey := range issueTypesByProject {
		projectKeys = append(projectKeys, projectKey)
	}
	sort.Strings(projectKeys)

	for _, projectKey := range projectKeys {
		keys, err := lister.ListXrayTestKeys(ctx, projectKey, issueTypeIDs, openOnly)
		if err != nil {
			slog.Debug("Xray Test discovery unavailable",
				slog.String("component", "jira"),
				slog.String("project", projectKey),
				slog.Any("error", err))
			if result.TotalTests == 0 {
				result.DetectionStatus = "unavailable"
			}
			result.WarningCode = "XRAY_TEST_DISCOVERY_INCOMPLETE"
			continue
		}
		if len(keys) == 0 {
			continue
		}
		result.TotalTests += len(keys)
		result.Projects = append(result.Projects, JiraXrayProjectAnalysis{
			ProjectKey: projectKey,
			TestCount:  len(keys),
		})
	}

	if result.TotalTests > 0 {
		result.DetectionStatus = "detected"
	}
	return result
}

func uniqueJiraIssueTypes(byProject map[string][]jira.JiraIssueType) []jira.JiraIssueType {
	seen := make(map[string]struct{})
	var issueTypes []jira.JiraIssueType
	for _, projectIssueTypes := range byProject {
		for _, issueType := range projectIssueTypes {
			if issueType.ID == "" {
				continue
			}
			if _, exists := seen[issueType.ID]; exists {
				continue
			}
			seen[issueType.ID] = struct{}{}
			issueTypes = append(issueTypes, issueType)
		}
	}
	sort.Slice(issueTypes, func(i, j int) bool {
		return issueTypes[i].ID < issueTypes[j].ID
	})
	return issueTypes
}
