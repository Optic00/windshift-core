package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// accessibleWorkspaceIDs returns the workspace IDs the user can read.
//
// Delegates to PermissionService.AccessibleWorkspaceIDs, the canonical
// gated-aware check: it enumerates active workspaces and keeps only those where
// the user has item.view permission, so a workspace flipped into gated mode (by
// any explicit role assignment) stays hidden from non-members. This re-establishes
// the gate that the deleted MCP per-family handlers enforced via canViewItem.
//
// Called once per MCP request when building the aitools.Env.
func (ms *MCPServer) accessibleWorkspaceIDs(userID int) ([]int, error) {
	return ms.deps.PermissionService.AccessibleWorkspaceIDs(userID)
}

// errNoAuth returns a standard auth error for tool handlers.
func errNoAuth() *mcp.CallToolResult {
	return toolError("authentication required")
}

// errInternal returns a tool error for internal failures.
func errInternal(op string, err error) *mcp.CallToolResult {
	return toolErrorf("failed to %s: %v", op, err)
}
