package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const openAIResponsesPath = "/v1/responses"

// openAIResponsesClient implements OpenAI's native Responses API. It keeps
// Windshift's public completion contract provider-neutral and confines the
// Responses item model to this adapter.
type openAIResponsesClient struct {
	endpoint       string
	model          string
	apiKey         string
	providerConfig string
	http           *http.Client
}

func newOpenAIResponsesClient(baseURL, model, apiKey, providerConfig string, timeout time.Duration) *openAIResponsesClient {
	return &openAIResponsesClient{
		endpoint:       baseURL,
		model:          model,
		apiKey:         apiKey,
		providerConfig: providerConfig,
		http:           newAdminConfiguredHTTPClient(timeout),
	}
}

func (c *openAIResponsesClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	body, err := openAIResponsesBody(req, c.model)
	if err != nil {
		return nil, err
	}
	if err := MergeProviderConfig(body, c.providerConfig); err != nil {
		return nil, err
	}
	return postOpenAIResponse(ctx, c.http, joinProviderPath(c.endpoint, openAIResponsesPath), c.apiKey, body)
}

func (c *openAIResponsesClient) Health(ctx context.Context) error {
	_, err := c.Complete(ctx, CompletionRequest{
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 1,
	})
	if err != nil {
		if isCompletionTokenLimitError(err) {
			return nil
		}
		return fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	return nil
}

func (c *openAIResponsesClient) Available() bool { return true }

func openAIResponsesBody(req CompletionRequest, model string) (map[string]interface{}, error) {
	input, err := openAIResponseInput(req.Messages)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"input": input,
		// Responses are stored by default. Windshift carries the typed output
		// items itself, so upstream persistence is unnecessary.
		"store": false,
	}
	if model == "" {
		model = req.Model
	}
	if model != "" {
		body["model"] = model
	}
	if req.Temperature != 0 {
		body["temperature"] = req.Temperature
	}
	if req.MaxTokens != 0 {
		body[responsesTokenParameter] = req.MaxTokens
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, tool := range req.Tools {
			if tool.Type != "function" {
				return nil, fmt.Errorf("unsupported OpenAI Responses tool type %q", tool.Type)
			}
			definition := map[string]interface{}{
				"type":        "function",
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters":  tool.Function.Parameters,
			}
			if tool.Function.Strict {
				definition["strict"] = true
			}
			tools = append(tools, definition)
		}
		body["tools"] = tools
		if req.ToolChoice != nil {
			body["tool_choice"] = openAIResponsesToolChoice(req.ToolChoice)
		}
	}
	if req.StructuredOutput != nil && len(req.StructuredOutput.Schema) > 0 {
		var schema interface{}
		if err := json.Unmarshal(req.StructuredOutput.Schema, &schema); err != nil {
			return nil, fmt.Errorf("invalid structured output schema: %w", err)
		}
		body["text"] = map[string]interface{}{
			"format": map[string]interface{}{
				"type":   "json_schema",
				"name":   req.StructuredOutput.SchemaName,
				"schema": schema,
				"strict": req.StructuredOutput.Strict,
			},
		}
	}
	return body, nil
}

func openAIResponsesToolChoice(choice interface{}) interface{} {
	object, ok := choice.(map[string]interface{})
	if !ok {
		return choice
	}
	function, ok := object["function"].(map[string]interface{})
	if !ok {
		return choice
	}
	name, ok := function["name"].(string)
	if !ok {
		return choice
	}
	return map[string]interface{}{"type": "function", "name": name}
}

func openAIResponseInput(messages []Message) ([]interface{}, error) {
	items := make([]interface{}, 0, len(messages))
	for _, message := range messages {
		if len(message.ProviderState) > 0 {
			var providerItems []interface{}
			if err := json.Unmarshal(message.ProviderState, &providerItems); err != nil {
				return nil, fmt.Errorf("decode OpenAI continuation state: %w", err)
			}
			items = append(items, providerItems...)
			continue
		}

		switch message.Role {
		case "tool":
			items = append(items, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": message.ToolCallID,
				"output":  message.Content,
			})
		case "assistant":
			if message.Content != "" {
				items = append(items, map[string]interface{}{"role": message.Role, "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]interface{}{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		default:
			items = append(items, map[string]interface{}{"role": message.Role, "content": message.Content})
		}
	}
	return items, nil
}

type openAIResponseEnvelope struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"`
	CreatedAt int64             `json:"created_at"`
	Status    string            `json:"status"`
	Output    []json.RawMessage `json:"output"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

func postOpenAIResponse(ctx context.Context, hc *http.Client, url, apiKey string, body map[string]interface{}) (*CompletionResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := hc.Do(httpReq) //nolint:gosec // URL from server-configured LLM endpoint
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, ErrServiceNotReady
	}
	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("%w: status %d; failed to read response body: %v", ErrAPIError, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("%w: status %d - %s", ErrAPIError, resp.StatusCode, string(respBody))
	}

	var envelope openAIResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return normalizeOpenAIResponse(envelope)
}

func normalizeOpenAIResponse(envelope openAIResponseEnvelope) (*CompletionResponse, error) {
	if envelope.Status == "failed" {
		if envelope.Error != nil {
			return nil, fmt.Errorf("%w: OpenAI response failed (%s): %s", ErrAPIError, envelope.Error.Code, envelope.Error.Message)
		}
		return nil, fmt.Errorf("%w: OpenAI response failed", ErrAPIError)
	}
	message := Message{Role: "assistant"}
	providerState, err := json.Marshal(envelope.Output)
	if err != nil {
		return nil, fmt.Errorf("preserve OpenAI continuation state: %w", err)
	}
	message.ProviderState = providerState

	for _, rawItem := range envelope.Output {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawItem, &header); err != nil {
			return nil, fmt.Errorf("decode OpenAI output item: %w", err)
		}
		switch header.Type {
		case "message":
			var item struct {
				Content []struct {
					Type    string `json:"type"`
					Text    string `json:"text"`
					Refusal string `json:"refusal"`
				} `json:"content"`
			}
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return nil, fmt.Errorf("decode OpenAI message item: %w", err)
			}
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					message.Content += content.Text
				case "refusal":
					message.Content += content.Refusal
				}
			}
		case "function_call":
			var item struct {
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return nil, fmt.Errorf("decode OpenAI function call item: %w", err)
			}
			message.ToolCalls = append(message.ToolCalls, ToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: FunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		}
	}

	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	} else if envelope.Status == "incomplete" {
		finishReason = "incomplete"
		if envelope.IncompleteDetails != nil {
			switch envelope.IncompleteDetails.Reason {
			case "max_output_tokens":
				finishReason = "length"
			case "content_filter":
				finishReason = "content_filter"
			}
		}
	}
	return &CompletionResponse{
		ID:      envelope.ID,
		Object:  envelope.Object,
		Created: envelope.CreatedAt,
		Choices: []Choice{{Index: 0, Message: message, FinishReason: finishReason}},
		Usage: Usage{
			PromptTokens:     envelope.Usage.InputTokens,
			CompletionTokens: envelope.Usage.OutputTokens,
			TotalTokens:      envelope.Usage.TotalTokens,
		},
	}, nil
}
