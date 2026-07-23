package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"windshift/internal/aitools"
	"windshift/internal/models"
)

// registerAITools registers every tool from the shared aitools registry
// with the MCP server. We use the SDK's raw AddTool path (Server.AddTool,
// not the generic mcp.AddTool) so we can pass the JSON Schema we already
// computed in the registry instead of having the SDK derive it from a
// typed In parameter. This keeps the schema source of truth on the
// aitools side: both surfaces see exactly the same schema bytes.
//
// The trade-off is that we do unmarshalling and validation ourselves;
// good enough for now since the registry produces well-formed schemas.
func (ms *MCPServer) registerAITools() {
	for _, e := range aitools.Default.All() {
		entry := e // capture per iteration
		ms.server.AddTool(&mcp.Tool{
			Name:        entry.Name,
			Description: entry.Description,
			InputSchema: entry.Schema,
			Annotations: toolAnnotations(entry),
			Meta: mcp.Meta{
				"required_scopes": append([]string(nil), entry.Scopes...),
			},
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			user := userFromContext(ctx)
			if user == nil {
				return errNoAuth(), nil
			}
			// Enforce the tool's declared token scopes (Entry.Scopes).
			// mcp:access (checked by the auth middleware) only opens the
			// surface; without this check a default-mint token could reach
			// tools its scope set excludes on the REST surface (e.g.
			// delete_item without items:delete, create_action without
			// actions:write). The in-product chat adapter never goes
			// through here — cookie sessions carry no token, so its
			// behavior is unchanged.
			if res, ok := ms.checkToolScopes(ctx, entry); !ok {
				return res, nil
			}
			env, err := ms.buildEnv(user)
			if err != nil {
				return errInternal("build env", err), nil
			}
			parsed := entry.NewArgs()
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, parsed); err != nil {
					return toolErrorf("invalid arguments: %v", err), nil
				}
			}
			out, err := entry.Run(ctx, env, parsed)
			if err != nil {
				return errInternal(entry.Name, err), nil
			}
			b, err := json.Marshal(out)
			if err != nil {
				return toolErrorf("marshal result: %v", err), nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
			}, nil
		})
	}
}

func toolAnnotations(entry aitools.Entry) *mcp.ToolAnnotations {
	readOnly := true
	destructive := false
	for _, scope := range entry.Scopes {
		if !strings.HasSuffix(scope, ":read") {
			readOnly = false
		}
		if strings.HasSuffix(scope, ":delete") {
			destructive = true
		}
	}
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
	}
}

// checkToolScopes verifies the request's validated API token carries every
// scope the tool declared (aitools registrations mirror the v1 router's
// per-route gating; write scopes imply the matching read scope). Returns a
// tool-error result naming the missing scopes when the check fails. Fails
// closed when no token is in context — only bearerAuthMiddleware puts one
// there, so a missing token means the request bypassed token auth somehow.
func (ms *MCPServer) checkToolScopes(ctx context.Context, entry aitools.Entry) (*mcp.CallToolResult, bool) {
	token := apiTokenFromContext(ctx)
	if token == nil {
		return errNoAuth(), false
	}
	var missing []string
	for _, scope := range entry.Scopes {
		if !ms.deps.TokenManager.CheckTokenPermissions(token, []string{scope}) {
			missing = append(missing, scope)
		}
	}
	if len(missing) > 0 {
		return toolErrorf("token missing required scope: %s", strings.Join(missing, ", ")), false
	}
	return nil, true
}

// buildEnv constructs an aitools.Env scoped to the calling user. Permissions
// are resolved fresh on each call (no per-session caching) — fine because
// MCP requests are usually one-shot.
func (ms *MCPServer) buildEnv(user *models.User) (*aitools.Env, error) {
	wsIDs, err := ms.accessibleWorkspaceIDs(user.ID)
	if err != nil {
		return nil, err
	}
	return &aitools.Env{
		DB:                     ms.deps.DB,
		UserID:                 user.ID,
		Username:               user.FullName,
		Source:                 aitools.SourceMCP,
		AccessibleWorkspaceIDs: wsIDs,
		PermService:            ms.deps.PermissionService,
		TimePermService:        ms.deps.TimePermissionService,
		TimerService:           ms.deps.TimerService,
		CommentService:         ms.deps.CommentService,
		ItemDeletionService:    ms.deps.ItemDeletionService,
		PageApplicationService: ms.deps.PageApplicationService,
		ActionService:          ms.deps.ActionService,
	}, nil
}
