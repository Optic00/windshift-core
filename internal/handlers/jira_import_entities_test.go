package handlers

import (
	"reflect"
	"testing"

	"windshift/internal/jira"
)

func TestJiraPreservationLabels(t *testing.T) {
	issue := &jira.JiraIssue{
		Fields: jira.JiraIssueFields{
			Components: []jira.JiraComponent{
				{Name: "API"},
				{Name: ""},
				{Name: " Frontend "},
			},
			Versions: []jira.JiraVersion{
				{Name: "1.0"},
				{Name: " 2.0 "},
				{Name: ""},
			},
		},
	}

	got := jiraPreservationLabels(issue)
	want := []string{"component:API", "component:Frontend", "affects:1.0", "affects:2.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("jiraPreservationLabels() = %#v, want %#v", got, want)
	}
}

func TestAffectedVersionOptionValuesPreservesAllMappedVersions(t *testing.T) {
	issue := &jira.JiraIssue{
		Fields: jira.JiraIssueFields{
			Versions: []jira.JiraVersion{
				{ID: "missing", Name: "Missing"},
				{ID: "v2", Name: "2.0"},
				{ID: "v3", Name: "3.0"},
				{ID: "v2", Name: "2.0 duplicate"},
			},
		},
	}

	got := affectedVersionOptionValues(issue, &jiraAffectsVersionCustomField{OptionIDsByJiraID: map[string]int{"v2": 22, "v3": 33}})
	want := []int{22, 33}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("affectedVersionOptionValues() = %#v, want %#v", got, want)
	}
}

func TestExtractJiraAssetCustomFieldValue(t *testing.T) {
	issueFields := &jira.JiraIssueFields{CustomFields: map[string]interface{}{
		"customfield_100": []interface{}{
			map[string]interface{}{"objectKey": "LAP-1", "label": "Laptop 1"},
			map[string]interface{}{"objectKey": "MON-2", "label": "Monitor 2"},
		},
	}}

	got, ok := extractCustomFieldValue(CustomFieldMapping{
		JiraID:        "customfield_100",
		WindshiftType: "asset",
		Action:        "create",
	}, issueFields, nil, nil)
	if !ok {
		t.Fatal("extractCustomFieldValue() did not return Jira asset value")
	}
	if got != "Laptop 1\nMonitor 2" {
		t.Fatalf("asset value = %#v, want newline-separated labels", got)
	}
}

func TestCollectUsersFromADFQueuesMentionOnlyUsers(t *testing.T) {
	adf := map[string]interface{}{
		"type": "doc",
		"content": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{
						"type": "mention",
						"attrs": map[string]interface{}{
							"id":   "acct-1",
							"text": "@Ada Lovelace",
						},
					},
					map[string]interface{}{
						"type": "mention",
						"attrs": map[string]interface{}{
							"id":   "already-mapped",
							"text": "@Existing User",
						},
					},
					map[string]interface{}{
						"type": "mention",
						"attrs": map[string]interface{}{
							"id":   "acct-1",
							"text": "@Ada Lovelace Duplicate",
						},
					},
				},
			},
		},
	}

	var users []JiraUserSummary
	collectUsersFromADF(adf, map[string]int{"already-mapped": 42}, &users, map[string]bool{})

	want := []JiraUserSummary{{AccountID: "acct-1", DisplayName: "Ada Lovelace"}}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("collectUsersFromADF() = %#v, want %#v", users, want)
	}
}
