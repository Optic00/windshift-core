package mcp

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolError returns a CallToolResult representing an error.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// toolErrorf returns a formatted tool error.
func toolErrorf(format string, args ...any) *mcp.CallToolResult {
	return toolError(fmt.Sprintf(format, args...))
}
