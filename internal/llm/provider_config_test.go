package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"windshift/internal/utils"
)

func TestMergeProviderConfigJSONAddsFieldsWithoutOverwritingRequest(t *testing.T) {
	got, err := MergeProviderConfigJSON(
		[]byte(`{"model":"connection-model","messages":[{"role":"user","content":"hi"}]}`),
		`{"model":"ignored","provider":{"order":["anthropic"],"allow_fallbacks":false},"temperature":0.2}`,
	)
	if err != nil {
		t.Fatalf("MergeProviderConfigJSON() error = %v", err)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("merged body is not JSON: %v", err)
	}
	if string(body["model"]) != `"connection-model"` {
		t.Fatalf("model = %s, want connection model to win", body["model"])
	}
	if string(body["provider"]) != `{"order":["anthropic"],"allow_fallbacks":false}` {
		t.Fatalf("provider = %s", body["provider"])
	}
	if string(body["temperature"]) != `0.2` {
		t.Fatalf("temperature = %s", body["temperature"])
	}
}

func TestValidateProviderConfigRejectsNonObject(t *testing.T) {
	if err := ValidateProviderConfig(`["anthropic"]`); err == nil {
		t.Fatal("expected array provider_config to be rejected")
	}
	if err := ValidateProviderConfig(`{"provider":{"sort":"latency"}}`); err != nil {
		t.Fatalf("expected object provider_config to pass: %v", err)
	}
}

func TestOpenAIClientIncludesProviderConfig(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(ChatCompletionResponse{
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "ok"}}},
		})
	}))
	defer server.Close()

	utils.SetAllowLocalConnections(true)
	defer utils.SetAllowLocalConnections(false)

	client := newOpenAIClient(
		server.URL,
		"openrouter-model",
		"",
		`{"provider":{"only":["anthropic"],"allow_fallbacks":false},"model":"ignored"}`,
		time.Second,
		"/chat/completions",
	)
	if _, err := client.ChatCompletion(context.Background(), ChatCompletionRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if string(body["model"]) != `"openrouter-model"` {
		t.Fatalf("model = %s, want configured model to win", body["model"])
	}
	var provider map[string]json.RawMessage
	if err := json.Unmarshal(body["provider"], &provider); err != nil {
		t.Fatalf("provider is not JSON object: %v", err)
	}
	if string(provider["only"]) != `["anthropic"]` {
		t.Fatalf("provider.only = %s", provider["only"])
	}
	if string(provider["allow_fallbacks"]) != `false` {
		t.Fatalf("provider.allow_fallbacks = %s", provider["allow_fallbacks"])
	}
}
