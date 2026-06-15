package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

func adfFromJSON(t *testing.T, src string) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(src), &out); err != nil {
		t.Fatalf("decode ADF fixture: %v", err)
	}
	return out
}

func TestConvertADFComplexBlocksToMarkdown(t *testing.T) {
	adf := adfFromJSON(t, `{
		"type":"doc",
		"version":1,
		"content":[
			{"type":"table","content":[
				{"type":"tableRow","content":[
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Name"}]}]},
					{"type":"tableHeader","content":[{"type":"paragraph","content":[{"type":"text","text":"Notes"}]}]}
				]},
				{"type":"tableRow","content":[
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"API"}]}]},
					{"type":"tableCell","content":[{"type":"paragraph","content":[{"type":"text","text":"uses | pipes"}]}]}
				]}
			]},
			{"type":"panel","attrs":{"panelType":"warning"},"content":[{"type":"paragraph","content":[{"type":"text","text":"Check this"}]}]},
			{"type":"taskList","content":[
				{"type":"taskItem","attrs":{"state":"DONE"},"content":[{"type":"text","text":"Ship it"}]},
				{"type":"taskItem","attrs":{"state":"TODO"},"content":[{"type":"text","text":"Document it"}]}
			]}
		]
	}`)

	got := ConvertADFToMarkdownWithUsers(adf, nil)
	for _, want := range []string{
		"| Name | Notes |",
		"| --- | --- |",
		"| API | uses \\| pipes |",
		"> [!WARNING]",
		"> Check this",
		"- [x] Ship it",
		"- [ ] Document it",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("converted markdown missing %q\nGot:\n%s", want, got)
		}
	}
}

func TestConvertADFInlineRichNodesToMarkdown(t *testing.T) {
	adf := adfFromJSON(t, `{
		"type":"doc",
		"version":1,
		"content":[{"type":"paragraph","content":[
			{"type":"inlineCard","attrs":{"url":"https://example.test/card"}},
			{"type":"text","text":" "},
			{"type":"status","attrs":{"text":"Blocked","color":"red"}},
			{"type":"text","text":" "},
			{"type":"emoji","attrs":{"shortName":":rocket:","text":"🚀"}},
			{"type":"text","text":" "},
			{"type":"date","attrs":{"timestamp":"1735689600000"}},
			{"type":"text","text":" "},
			{"type":"mention","attrs":{"id":"acct-1","text":"@Display Name"}}
		]}]
	}`)

	got := ConvertADFToMarkdownWithUsers(adf, func(accountID string) string {
		if accountID == "acct-1" {
			return "display.name"
		}
		return ""
	})

	want := "[https://example.test/card](https://example.test/card) [Blocked] 🚀 2025-01-01 @display.name"
	if !strings.Contains(got, want) {
		t.Fatalf("converted markdown missing %q\nGot:\n%s", want, got)
	}
}
