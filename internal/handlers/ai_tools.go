package handlers

import (
	"context"

	"windshift/internal/aitooladapter"
	"windshift/internal/aitools"
	"windshift/internal/llm"
)

// ToolExecutor executes tool calls on behalf of the agentic chat loop.
// It enforces workspace access via a pre-computed list of accessible workspace IDs.
type ToolExecutor struct {
	adapter *aitooladapter.Executor
}

// NewToolExecutor creates a tool executor scoped to the given tool environment.
func NewToolExecutor(env *aitools.Env) *ToolExecutor {
	return &ToolExecutor{adapter: aitooladapter.NewExecutor(env, aitools.Default.All())}
}

// Execute dispatches a tool call by name and returns the JSON result.
// All tools live in the shared internal/aitools/ registry; this is now
// just protocol glue.
func (e *ToolExecutor) Execute(ctx context.Context, name, arguments string) (string, error) {
	return e.adapter.Execute(ctx, name, arguments)
}

// BuildLLMTools merges the legacy hand-written tool definitions in
// llm.BuiltinTools() with the typed registry in aitools.Default. Every
// registry entry is exposed to the agent loop with the JSON Schema
// derived from its Args struct.
func BuildLLMTools() []llm.ToolDefinition {
	out := llm.BuiltinTools()
	out = append(out, aitooladapter.BuildTools(aitools.Default.All())...)
	return out
}
