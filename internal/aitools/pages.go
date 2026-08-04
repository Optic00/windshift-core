package aitools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/auth"
	"windshift/internal/models"
	"windshift/internal/services"
)

// --- arg types ---

type searchKnowledgeArgs struct {
	Query       string `json:"query" jsonschema:"Free-text search query"`
	WorkspaceID int    `json:"workspace_id" jsonschema:"Workspace to search in"`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of results (default 25, max 100)"`
}

type getPageArgs struct {
	PageID int `json:"page_id" jsonschema:"Page ID to fetch"`
}

type listPagesArgs struct {
	WorkspaceID int  `json:"workspace_id" jsonschema:"Workspace to list pages from"`
	ParentID    *int `json:"parent_id,omitempty" jsonschema:"Optional parent page ID; omit for root pages"`
}

type createPageArgs struct {
	WorkspaceID int    `json:"workspace_id" jsonschema:"Workspace to create the page in"`
	ParentID    *int   `json:"parent_id,omitempty" jsonschema:"Optional parent page ID; omit for a root page"`
	Title       string `json:"title" jsonschema:"Page title"`
	Content     string `json:"content,omitempty" jsonschema:"Markdown content"`
	IsHome      bool   `json:"is_home,omitempty" jsonschema:"Whether this is the workspace home page"`
}

type updatePageArgs struct {
	PageID              int     `json:"page_id" jsonschema:"Page ID to update"`
	Title               *string `json:"title,omitempty" jsonschema:"New title; omit to keep current title"`
	Content             *string `json:"content,omitempty" jsonschema:"New Markdown content; omit to keep current content"`
	ExpectedContentHash *string `json:"expected_content_hash,omitempty" jsonschema:"Optional content hash returned by get_page; stale hashes reject the update"`
}

type movePageArgs struct {
	PageID                 int  `json:"page_id" jsonschema:"Page ID to move"`
	DestinationWorkspaceID *int `json:"destination_workspace_id,omitempty" jsonschema:"Destination workspace ID; omit to move within the current workspace"`
	ParentID               *int `json:"parent_id" jsonschema:"New parent ID; null for workspace root"`
	PrevSiblingID          *int `json:"prev_sibling_id,omitempty" jsonschema:"Place page after this sibling"`
	NextSiblingID          *int `json:"next_sibling_id,omitempty" jsonschema:"Place page before this sibling"`
}

type archivePageArgs struct {
	PageID int `json:"page_id" jsonschema:"Page ID to archive with its subtree"`
}

type restorePageRevisionArgs struct {
	PageID     int `json:"page_id" jsonschema:"Page ID to restore"`
	RevisionID int `json:"revision_id" jsonschema:"Revision ID to restore from"`
}

type getPagePermissionsArgs struct {
	PageID int `json:"page_id" jsonschema:"Page ID whose ACL should be fetched"`
}

type grantPagePermissionArgs struct {
	PageID          int    `json:"page_id" jsonschema:"Page ID to grant on"`
	PrincipalType   string `json:"principal_type" jsonschema:"Principal type: user, group, or role"`
	PrincipalID     int    `json:"principal_id" jsonschema:"Principal ID"`
	PermissionLevel string `json:"permission_level" jsonschema:"Permission level: view, edit, or admin"`
}

type revokePagePermissionArgs struct {
	PageID       int `json:"page_id" jsonschema:"Page ID"`
	PermissionID int `json:"permission_id" jsonschema:"ACL row ID to revoke"`
}

type setPageInheritanceArgs struct {
	PageID             int  `json:"page_id" jsonschema:"Page ID"`
	InheritPermissions bool `json:"inherit_permissions" jsonschema:"Whether this page should inherit permissions from ancestors"`
}

// --- output shapes ---

type searchKnowledgeOut struct {
	Results []services.KnowledgeResult `json:"results"`
}

type pageDTO struct {
	ID          int    `json:"id"`
	WorkspaceID int    `json:"workspace_id"`
	ParentID    *int   `json:"parent_id,omitempty"`
	Title       string `json:"title"`
	Slug        string `json:"slug"`
	ContentHash string `json:"content_hash"`
	Path        string `json:"path"`
	Depth       int    `json:"depth"`
	URL         string `json:"url"`
}

type getPageOut struct {
	Page    pageDTO `json:"page"`
	Content string  `json:"content"`
	Excerpt string  `json:"excerpt"`
}

type listPagesOut struct {
	Pages []pageDTO `json:"pages"`
}

type pagePermissionOut struct {
	ID              int    `json:"id"`
	PageID          int    `json:"page_id"`
	PrincipalType   string `json:"principal_type"`
	PrincipalID     int    `json:"principal_id"`
	PermissionLevel string `json:"permission_level"`
}

type pagePermissionsOut struct {
	PageID             int                 `json:"page_id"`
	InheritPermissions bool                `json:"inherit_permissions"`
	EffectiveLevel     string              `json:"effective_level"`
	ACL                []pagePermissionOut `json:"acl"`
}

// --- registry hookup ---
//
// Page tools intentionally mirror the HTTP authorization model: workspace
// membership from Env first, then PagePermissionService for page-level ACLs.

func init() {
	Register(Default, Tool[searchKnowledgeArgs]{
		Name:   "search_knowledge",
		Group:  CapabilityKnowledgeDiagrams,
		Access: AccessRead,
		Risk:   RiskLow,
		Description: "Search workspace knowledge pages by free-text query. Returns " +
			"permission-filtered snippets with title, heading path, and URL. " +
			"Call this before answering questions about internal docs, procedures, " +
			"policies, or project-specific knowledge.",
		Scopes: []string{auth.ScopePagesRead},
		Run: func(_ context.Context, env *Env, args searchKnowledgeArgs) (any, error) {
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			retrieval := services.NewKnowledgeRetrievalService(env.DB, services.NewPagePermissionService(env.DB, env.PermService))
			results, err := retrieval.Search(services.SearchInput{
				UserID:      env.UserID,
				WorkspaceID: args.WorkspaceID,
				Query:       args.Query,
				Limit:       args.Limit,
			})
			if err != nil {
				return nil, err
			}
			if results == nil {
				results = []services.KnowledgeResult{}
			}
			return searchKnowledgeOut{Results: results}, nil
		},
	})

	Register(Default, Tool[getPageArgs]{
		Name:        "get_page",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessRead,
		Risk:        RiskLow,
		Description: "Fetch a single workspace knowledge page by id. Returns the title, Markdown content, and excerpt if the caller can view it.",
		Scopes:      []string{auth.ScopePagesRead},
		Run: func(_ context.Context, env *Env, args getPageArgs) (any, error) {
			pageSvc := services.NewPageService(env.DB)
			pageAuth := services.NewPagePermissionService(env.DB, env.PermService)
			page, err := pageSvc.GetByID(args.PageID)
			if err != nil {
				return map[string]string{"error": "page not found"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if !env.HasWorkspaceAccess(page.WorkspaceID) {
				return map[string]string{"error": "page not found"}, nil
			}
			can, err := pageAuth.Can(env.UserID, page.WorkspaceID, page.ID, services.PageOpView)
			if err != nil {
				return nil, err
			}
			if !can {
				return map[string]string{"error": "page not found"}, nil
			}
			return getPageOut{
				Page: pageDTO{
					ID:          page.ID,
					WorkspaceID: page.WorkspaceID,
					ParentID:    page.ParentID,
					Title:       page.Title,
					Slug:        page.Slug,
					Path:        page.Path,
					Depth:       page.Depth,
					URL:         fmt.Sprintf("/workspaces/%d/pages/%d", page.WorkspaceID, page.ID),
				},
				Content: page.Content,
				Excerpt: page.Excerpt,
			}, nil
		},
	})

	Register(Default, Tool[listPagesArgs]{
		Name:        "list_pages",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessRead,
		Risk:        RiskLow,
		Description: "List workspace knowledge pages (optionally scoped to a parent). Returns only pages the caller can view.",
		Scopes:      []string{auth.ScopePagesRead},
		Run: func(_ context.Context, env *Env, args listPagesArgs) (any, error) {
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			pageSvc := services.NewPageService(env.DB)
			pageAuth := services.NewPagePermissionService(env.DB, env.PermService)

			var pages = make([]pageDTO, 0)
			children, err := pageSvc.ListChildren(args.WorkspaceID, args.ParentID)
			if err != nil {
				return nil, err
			}
			for _, p := range children {
				can, cerr := pageAuth.Can(env.UserID, args.WorkspaceID, p.ID, services.PageOpView)
				if cerr != nil {
					return nil, cerr
				}
				if !can {
					continue
				}
				pages = append(pages, pageToDTO(&p))
			}
			return listPagesOut{Pages: pages}, nil
		},
	})

	Register(Default, Tool[createPageArgs]{
		Name:        "create_page",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessWrite,
		Risk:        RiskMedium,
		Description: "Create a workspace knowledge page. Requires workspace page.create/page.admin/workspace.admin and parent edit access when parent_id is set.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args createPageArgs) (any, error) {
			if strings.TrimSpace(args.Title) == "" {
				return map[string]string{"error": "title is required"}, nil
			}
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			page, err := pageApplicationService(env).Create(pageAuditActor(env), services.CreatePageInput{WorkspaceID: args.WorkspaceID, ParentID: args.ParentID, Title: args.Title, Content: args.Content, IsHome: args.IsHome})
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"page": pageToDTO(page), "content": page.Content}, nil
		},
	})

	Register(Default, Tool[updatePageArgs]{
		Name:        "update_page",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessWrite,
		Risk:        RiskMedium,
		Description: "Update a page title and/or Markdown content. Requires edit access to the page.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args updatePageArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			if args.Title == nil && args.Content == nil {
				return map[string]string{"error": "no fields to update"}, nil
			}
			updated, err := pageApplicationService(env).Update(pageAuditActor(env), page.WorkspaceID, services.PageApplicationUpdateInput{
				ID:                  page.ID,
				Title:               args.Title,
				Content:             args.Content,
				ExpectedContentHash: args.ExpectedContentHash,
			})
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"page": pageToDTO(updated), "content": updated.Content}, nil
		},
	})

	Register(Default, Tool[movePageArgs]{
		Name:        "move_page",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessWrite,
		Risk:        RiskMedium,
		Description: "Move or reorder a page. Requires edit access to the moved page and destination parent.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args movePageArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			moved, err := pageApplicationService(env).Move(pageAuditActor(env), page.WorkspaceID, page.ID, args.DestinationWorkspaceID, args.ParentID, args.PrevSiblingID, args.NextSiblingID)
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"page": pageToDTO(moved)}, nil
		},
	})

	Register(Default, Tool[archivePageArgs]{
		Name:        "archive_page",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessDestructive,
		Risk:        RiskHigh,
		Description: "Archive a page and its subtree. Requires page.admin on every subtree page and workspace page.delete.",
		Scopes:      []string{auth.ScopePagesDelete},
		Run: func(_ context.Context, env *Env, args archivePageArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			archived, err := pageApplicationService(env).Archive(pageAuditActor(env), page.WorkspaceID, page.ID)
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"archived": true, "page_id": archived.ID}, nil
		},
	})

	Register(Default, Tool[restorePageRevisionArgs]{
		Name:        "restore_page_revision",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessWrite,
		Risk:        RiskMedium,
		Description: "Restore a page's title/content from a revision. Also unarchives the page when the target is archived.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args restorePageRevisionArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			restored, err := pageApplicationService(env).Restore(pageAuditActor(env), page.WorkspaceID, page.ID, args.RevisionID)
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"page": pageToDTO(restored), "content": restored.Content}, nil
		},
	})

	Register(Default, Tool[getPagePermissionsArgs]{
		Name:        "get_page_permissions",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessRead,
		Risk:        RiskLow,
		Description: "Read a page's inherit flag, current caller effective level, and explicit ACL rows.",
		Scopes:      []string{auth.ScopePagesRead},
		Run: func(_ context.Context, env *Env, args getPagePermissionsArgs) (any, error) {
			pageSvc := services.NewPageService(env.DB)
			page, ok, err := loadAuthorizedPage(env, pageSvc, args.PageID, services.PageOpView)
			if err != nil || !ok {
				return toolPageAuthResult(ok, err)
			}
			acl, err := pageSvc.ListOwnACL(page.ID)
			if err != nil {
				return nil, err
			}
			return pagePermissionsPayload(env, page, acl)
		},
	})

	Register(Default, Tool[grantPagePermissionArgs]{
		Name:        "grant_page_permission",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessAdmin,
		Risk:        RiskHigh,
		Description: "Grant a user, group, or role view/edit/admin access on a page. Requires page.admin.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args grantPagePermissionArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			perm, err := pageApplicationService(env).GrantPermission(pageAuditActor(env), page.WorkspaceID, page.ID, args.PrincipalType, args.PrincipalID, args.PermissionLevel)
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return permissionToOut(*perm), nil
		},
	})

	Register(Default, Tool[revokePagePermissionArgs]{
		Name:        "revoke_page_permission",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessAdmin,
		Risk:        RiskHigh,
		Description: "Revoke a page ACL row. Requires page.admin.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args revokePagePermissionArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			if err := pageApplicationService(env).RevokePermission(pageAuditActor(env), page.WorkspaceID, page.ID, args.PermissionID); err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"revoked": true, "permission_id": args.PermissionID}, nil
		},
	})

	Register(Default, Tool[setPageInheritanceArgs]{
		Name:        "set_page_inheritance",
		Group:       CapabilityKnowledgeDiagrams,
		Access:      AccessAdmin,
		Risk:        RiskHigh,
		Description: "Enable or disable permission inheritance on a page. Requires page.admin.",
		Scopes:      []string{auth.ScopePagesWrite},
		Run: func(_ context.Context, env *Env, args setPageInheritanceArgs) (any, error) {
			pageSvc := pageApplicationService(env).PageService()
			page, ok := loadWorkspacePage(env, pageSvc, args.PageID)
			if !ok {
				return toolPageAuthResult(false, nil)
			}
			updated, err := pageApplicationService(env).SetInheritance(pageAuditActor(env), page.WorkspaceID, page.ID, args.InheritPermissions)
			if err != nil {
				return pageMutationToolError(err), nil
			}
			return map[string]any{"page": pageToDTO(updated), "inherit_permissions": updated.InheritPermissions}, nil
		},
	})
}

func pageToDTO(page *models.Page) pageDTO {
	return pageDTO{
		ID:          page.ID,
		WorkspaceID: page.WorkspaceID,
		ParentID:    page.ParentID,
		Title:       page.Title,
		Slug:        page.Slug,
		ContentHash: page.ContentHash,
		Path:        page.Path,
		Depth:       page.Depth,
		URL:         fmt.Sprintf("/workspaces/%d/pages/%d", page.WorkspaceID, page.ID),
	}
}

func loadAuthorizedPage(env *Env, pageSvc *services.PageService, pageID int, op string) (*models.Page, bool, error) {
	page, err := pageSvc.GetByID(pageID)
	if err != nil {
		return nil, false, nil //nolint:nilerr // hide not-found as tool JSON, not a protocol error
	}
	if !env.HasWorkspaceAccess(page.WorkspaceID) {
		return nil, false, nil
	}
	pageAuth := services.NewPagePermissionService(env.DB, env.PermService)
	can, err := pageAuth.Can(env.UserID, page.WorkspaceID, page.ID, op)
	if err != nil || !can {
		return nil, false, err
	}
	return page, true, nil
}

func toolPageAuthResult(ok bool, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]string{"error": "page not found"}, nil
	}
	return nil, nil
}

func pageApplicationService(env *Env) *services.PageApplicationService {
	if env.PageApplicationService != nil {
		return env.PageApplicationService
	}
	pageService := services.NewPageService(env.DB)
	pageAuth := services.NewPagePermissionService(env.DB, env.PermService)
	return services.NewPageApplicationService(pageService, pageAuth)
}

func pageAuditActor(env *Env) services.AuditActor {
	return services.AuditActor{UserID: env.UserID, Username: env.Username, Source: env.Source}
}

// loadWorkspacePage performs the tool adapter's fresh accessible-workspace
// gate without duplicating operation-specific page ACL rules.
func loadWorkspacePage(env *Env, pageSvc *services.PageService, pageID int) (*models.Page, bool) {
	page, err := pageSvc.GetByID(pageID)
	if err != nil {
		return nil, false
	}
	if !env.HasWorkspaceAccess(page.WorkspaceID) {
		return nil, false
	}
	return page, true
}

func pageMutationToolError(err error) map[string]string {
	switch {
	case errors.Is(err, services.ErrPageMutationForbidden):
		return map[string]string{"error": "permission denied"}
	case errors.Is(err, services.ErrPageParentNotFound):
		return map[string]string{"error": "parent page not found"}
	case errors.Is(err, services.ErrPageNotFound):
		return map[string]string{"error": "page not found"}
	case errors.Is(err, services.ErrPageNoChanges):
		return map[string]string{"error": "no fields to update"}
	case errors.Is(err, services.ErrPageContentConflict):
		return map[string]string{"error": "page content changed since it was read"}
	default:
		return map[string]string{"error": err.Error()}
	}
}

func permissionToOut(p models.PagePermission) pagePermissionOut {
	return pagePermissionOut{ID: p.ID, PageID: p.PageID, PrincipalType: p.PrincipalType, PrincipalID: p.PrincipalID, PermissionLevel: p.PermissionLevel}
}

func pagePermissionsPayload(env *Env, page *models.Page, acl []models.PagePermission) (pagePermissionsOut, error) {
	pageAuth := services.NewPagePermissionService(env.DB, env.PermService)
	effective := ""
	for _, op := range []string{services.PageOpAdmin, services.PageOpEdit, services.PageOpView} {
		can, err := pageAuth.Can(env.UserID, page.WorkspaceID, page.ID, op)
		if err != nil {
			return pagePermissionsOut{}, err
		}
		if can {
			effective = op
			break
		}
	}
	out := pagePermissionsOut{PageID: page.ID, InheritPermissions: page.InheritPermissions, EffectiveLevel: effective, ACL: []pagePermissionOut{}}
	for _, row := range acl {
		out.ACL = append(out.ACL, permissionToOut(row))
	}
	return out, nil
}
