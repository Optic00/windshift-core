package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"windshift/internal/xray"
)

// XrayTestIssueTypeProperty is the Xray-owned Jira Cloud entity property that
// identifies the issue type used for Xray Tests. Names are deliberately not
// part of this contract because administrators may rename the type and other
// test-management products may use the same display name.
const XrayTestIssueTypeProperty = "com.xpandit.xray.issuetype.test"

// XrayTestIssueTypeDetector is implemented by Jira clients that can positively
// identify Xray-owned Test issue types.
type XrayTestIssueTypeDetector interface {
	DetectXrayTestIssueTypes(ctx context.Context, issueTypes []JiraIssueType) ([]string, error)
}

// XrayTestKeyLister is implemented by Jira clients that can positively list
// Xray Test issues in a project. For Cloud, issueTypeIDs must come from
// DetectXrayTestIssueTypes. Data Center obtains the list from Raven directly.
type XrayTestKeyLister interface {
	ListXrayTestKeys(ctx context.Context, projectKey string, issueTypeIDs []string, openOnly bool) ([]string, error)
}

// XrayTestStepReader reads manual steps from a colocated Xray installation.
type XrayTestStepReader interface {
	GetXrayTestSteps(ctx context.Context, issueKey string) ([]xray.Step, error)
}

// DetectXrayTestIssueTypes checks Xray's owned issue-type property directly.
// A missing property is a normal negative result. Any other upstream failure
// makes detection unavailable; callers must not fall back to display names.
func (c *cloudClient) DetectXrayTestIssueTypes(ctx context.Context, issueTypes []JiraIssueType) ([]string, error) {
	seen := make(map[string]struct{}, len(issueTypes))
	var detected []string
	for _, issueType := range issueTypes {
		if issueType.ID == "" {
			continue
		}
		if _, exists := seen[issueType.ID]; exists {
			continue
		}
		seen[issueType.ID] = struct{}{}

		propertyURL := c.baseURL + "/issuetype/" + url.PathEscape(issueType.ID) +
			"/properties/" + XrayTestIssueTypeProperty
		resp, err := c.do(ctx, http.MethodGet, propertyURL, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			responseErr := jiraErrorFromResponse(resp)
			_ = resp.Body.Close()
			return nil, responseErr
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read Xray issue-type property for %s: %w", issueType.ID, readErr)
		}
		owned, err := xrayIssueTypePropertyOwnsTest(body)
		if err != nil {
			return nil, fmt.Errorf("decode Xray issue-type property for %s: %w", issueType.ID, err)
		}
		if owned {
			detected = append(detected, issueType.ID)
		}
	}
	sort.Strings(detected)
	return detected, nil
}

// xrayIssueTypePropertyOwnsTest accepts both Jira's wrapped entity-property
// response and the direct property value returned by current Xray Cloud
// installations. Xray uses an empty object as the live value, so the exact
// Xray-owned property key plus a successful response is the decisive marker.
// An explicit boolean false remains a negative result for compatibility.
func xrayIssueTypePropertyOwnsTest(body []byte) (bool, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return false, err
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case map[string]any:
		if wrapped, exists := typed["value"]; exists {
			if enabled, ok := wrapped.(bool); ok {
				return enabled, nil
			}
		}
		return true, nil
	default:
		return true, nil
	}
}

// ListXrayTestKeys lists Cloud issues only after their issue type has been
// positively identified by Xray's entity property.
func (c *cloudClient) ListXrayTestKeys(ctx context.Context, projectKey string, issueTypeIDs []string, openOnly bool) ([]string, error) {
	if len(issueTypeIDs) == 0 {
		return []string{}, nil
	}
	jql := xrayTestJQL(projectKey, issueTypeIDs, openOnly)
	return c.GetAllIssueKeys(ctx, jql)
}

// ListXrayTestKeys asks Raven for Tests directly on Server/Data Center. A
// successful Raven response is the ownership proof; no Jira issue-type display
// name is consulted.
func (c *dataCenterClient) ListXrayTestKeys(ctx context.Context, projectKey string, _ []string, openOnly bool) ([]string, error) {
	jql := xrayTestJQL(projectKey, nil, openOnly)
	body, err := c.doXrayGet(ctx, c.xrayURL+"/test?jql="+url.QueryEscape(jql), "Xray Test export")
	if err != nil {
		return nil, err
	}
	keys, err := decodeXrayTestKeys(body)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// doXrayGet performs a Raven GET and returns the raw response body. The Raven
// payloads here are not plain JSON-decoded at the transport layer because
// Data Center releases differ on envelope shape, so callers decode.
func (c *dataCenterClient) doXrayGet(ctx context.Context, reqURL, what string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, jiraErrorFromResponse(resp)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", what, err)
	}
	return body, nil
}

// GetXrayTestSteps reads the manual Test steps from Raven and normalizes field
// names used by supported Server/Data Center releases.
func (c *dataCenterClient) GetXrayTestSteps(ctx context.Context, issueKey string) ([]xray.Step, error) {
	body, err := c.doXrayGet(ctx, c.xrayURL+"/test/"+url.PathEscape(issueKey)+"/step", "Xray Test steps")
	if err != nil {
		return nil, err
	}

	var direct []xrayDataCenterStep
	if err := json.Unmarshal(body, &direct); err != nil {
		var envelope struct {
			Steps []xrayDataCenterStep `json:"steps"`
		}
		if envelopeErr := json.Unmarshal(body, &envelope); envelopeErr != nil || envelope.Steps == nil {
			return nil, fmt.Errorf("decode Xray Test steps: %w", err)
		}
		direct = envelope.Steps
	}

	sort.SliceStable(direct, func(i, j int) bool {
		return direct[i].Index < direct[j].Index
	})
	steps := make([]xray.Step, 0, len(direct))
	for _, source := range direct {
		action := source.Step
		if action == "" {
			action = source.Action
		}
		expected := source.Result
		if expected == "" {
			expected = source.Expected
		}
		steps = append(steps, xray.Step{
			Action:   action,
			Data:     source.Data,
			Expected: expected,
		})
	}
	return steps, nil
}

type xrayDataCenterStep struct {
	Index    int    `json:"index"`
	Step     string `json:"step"`
	Action   string `json:"action"`
	Data     string `json:"data"`
	Result   string `json:"result"`
	Expected string `json:"expectedResult"`
}

func xrayTestJQL(projectKey string, issueTypeIDs []string, openOnly bool) string {
	clauses := []string{`project = "` + escapeJQLString(projectKey) + `"`}
	if len(issueTypeIDs) > 0 {
		quoted := make([]string, 0, len(issueTypeIDs))
		for _, id := range issueTypeIDs {
			if strings.TrimSpace(id) != "" {
				quoted = append(quoted, `"`+escapeJQLString(id)+`"`)
			}
		}
		if len(quoted) > 0 {
			clauses = append(clauses, "issuetype IN ("+strings.Join(quoted, ", ")+")")
		}
	}
	if openOnly {
		clauses = append(clauses, "statusCategory != Done")
	}
	return strings.Join(clauses, " AND ") + " ORDER BY created ASC"
}

func escapeJQLString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

type xrayTestKey struct {
	Key     string `json:"key"`
	TestKey string `json:"testKey"`
}

func decodeXrayTestKeys(body []byte) ([]string, error) {
	var direct []xrayTestKey
	if err := json.Unmarshal(body, &direct); err == nil {
		return uniqueXrayTestKeys(direct), nil
	}

	var envelope struct {
		Tests   []xrayTestKey `json:"tests"`
		Results []xrayTestKey `json:"results"`
		Values  []xrayTestKey `json:"values"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode Xray Test export: %w", err)
	}
	switch {
	case envelope.Tests != nil:
		return uniqueXrayTestKeys(envelope.Tests), nil
	case envelope.Results != nil:
		return uniqueXrayTestKeys(envelope.Results), nil
	case envelope.Values != nil:
		return uniqueXrayTestKeys(envelope.Values), nil
	default:
		return nil, fmt.Errorf("decode Xray Test export: response contains no test list")
	}
}

func uniqueXrayTestKeys(tests []xrayTestKey) []string {
	seen := make(map[string]struct{}, len(tests))
	keys := make([]string, 0, len(tests))
	for _, test := range tests {
		key := strings.TrimSpace(test.Key)
		if key == "" {
			key = strings.TrimSpace(test.TestKey)
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}
