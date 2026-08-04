package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openai"
)

// Fantasy v0.40 exposes encrypted Responses reasoning metadata but currently
// drops reasoning parts while preparing a follow-up prompt. This narrow shim
// restores those opaque items before their matching function calls until the
// component performs the replay itself.
type reasoningReplay struct {
	beforeToolCall string
	item           json.RawMessage
}

type reasoningReplayState struct {
	mu        sync.Mutex
	items     []reasoningReplay
	overrides map[string]json.RawMessage
}

func (s *reasoningReplayState) set(items []reasoningReplay, overrides map[string]json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append([]reasoningReplay(nil), items...)
	s.overrides = make(map[string]json.RawMessage, len(overrides))
	for key, value := range overrides {
		s.overrides[key] = append(json.RawMessage(nil), value...)
	}
}

func (s *reasoningReplayState) get() (items []reasoningReplay, overrides map[string]json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	overrides = make(map[string]json.RawMessage, len(s.overrides))
	for key, value := range s.overrides {
		overrides[key] = append(json.RawMessage(nil), value...)
	}
	return append(items, s.items...), overrides
}

func reasoningReplays(messages []Message) ([]reasoningReplay, error) {
	var replays []reasoningReplay
	for _, message := range messages {
		if message.Role != "assistant" || len(message.ProviderState) == 0 {
			continue
		}
		var preserved fantasy.Message
		if err := json.Unmarshal(message.ProviderState, &preserved); err != nil {
			return nil, fmt.Errorf("decode provider continuation: %w", err)
		}
		var nextToolCall string
		for _, part := range preserved.Content {
			if toolCall, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
				nextToolCall = toolCall.ToolCallID
				break
			}
		}
		if nextToolCall == "" {
			continue
		}
		for _, part := range preserved.Content {
			reasoning, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](part)
			if !ok {
				continue
			}
			metadata := openai.GetReasoningMetadata(reasoning.ProviderOptions)
			if metadata == nil || metadata.ItemID == "" || metadata.EncryptedContent == nil {
				continue
			}
			summary := make([]map[string]string, 0, len(metadata.Summary))
			for _, text := range metadata.Summary {
				summary = append(summary, map[string]string{"type": "summary_text", "text": text})
			}
			item, err := json.Marshal(map[string]any{
				"type":              "reasoning",
				"id":                metadata.ItemID,
				"summary":           summary,
				"encrypted_content": *metadata.EncryptedContent,
			})
			if err != nil {
				return nil, err
			}
			replays = append(replays, reasoningReplay{beforeToolCall: nextToolCall, item: item})
		}
	}
	return replays, nil
}

type reasoningReplayTransport struct {
	next  http.RoundTripper
	state *reasoningReplayState
}

func (t *reasoningReplayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.state.inject(request); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(request)
}

func (s *reasoningReplayState) inject(request *http.Request) error {
	items, overrides := s.get()
	if (len(items) == 0 && len(overrides) == 0) || request.Body == nil || !strings.HasSuffix(request.URL.Path, "/responses") {
		return nil
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return err
	}
	_ = request.Body.Close()
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode Fantasy Responses request: %w", err)
	}
	for key, value := range overrides {
		envelope[key] = value
	}
	var input []json.RawMessage
	if err := json.Unmarshal(envelope["input"], &input); err != nil {
		return fmt.Errorf("decode Fantasy Responses input: %w", err)
	}
	existing := make(map[string]bool)
	for _, raw := range input {
		var item struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		_ = json.Unmarshal(raw, &item)
		if item.Type == "reasoning" {
			existing[item.ID] = true
		}
	}
	rewritten := make([]json.RawMessage, 0, len(input)+len(items))
	for _, raw := range input {
		var item struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		_ = json.Unmarshal(raw, &item)
		if item.Type == "function_call" {
			for _, replay := range items {
				var reasoning struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(replay.item, &reasoning)
				if replay.beforeToolCall == item.CallID && !existing[reasoning.ID] {
					rewritten = append(rewritten, replay.item)
					existing[reasoning.ID] = true
				}
			}
		}
		rewritten = append(rewritten, raw)
	}
	envelope["input"], err = json.Marshal(rewritten)
	if err != nil {
		return err
	}
	body, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return nil
}
