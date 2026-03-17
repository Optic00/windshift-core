package llm

import "encoding/json"

// Attachment holds a base64-encoded file to include in a message (e.g. a PDF for Anthropic document blocks).
type Attachment struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"` // base64-encoded
}

// Message represents a chat message in the OpenAI-compatible format.
type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"attachments,omitempty"`
	// Tool calling fields
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // for role="tool" messages
	Name       string     `json:"name,omitempty"`         // function name for role="tool" messages
}

// ToolDefinition describes a tool the LLM can call.
type ToolDefinition struct {
	Type     string       `json:"type"` // "function"
	Function FunctionDef  `json:"function"`
}

// FunctionDef describes a callable function.
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall represents an LLM's request to call a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the function name and arguments from a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// StructuredOutputConfig configures structured output constraints.
// The Schema is a JSON Schema that the response must conform to.
type StructuredOutputConfig struct {
	Schema     json.RawMessage `json:"schema,omitempty"`
	SchemaName string          `json:"schema_name,omitempty"`
	Strict     bool            `json:"strict,omitempty"`
}

// ChatCompletionRequest is the request body for /v1/chat/completions.
type ChatCompletionRequest struct {
	Model            string                  `json:"model,omitempty"`
	Messages         []Message               `json:"messages"`
	Temperature      float64                 `json:"temperature,omitempty"`
	MaxTokens        int                     `json:"max_tokens,omitempty"`
	StructuredOutput *StructuredOutputConfig `json:"structured_output,omitempty"`
	// Tool calling fields
	Tools      []ToolDefinition `json:"tools,omitempty"`
	ToolChoice interface{}      `json:"tool_choice,omitempty"` // "auto", "none", or {"type":"function","function":{"name":"..."}}
}

// ChatCompletionResponse is the response from /v1/chat/completions.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage contains token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
