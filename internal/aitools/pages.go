package aitools

import (
	"context"
	"fmt"

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

// --- registry hookup ---
//
// search_knowledge / get_page / list_pages are all read-only. Phase 1 does
// not register write tools (create_page, update_page) — those land alongside
// the page-editing UI in a later slice once we have audit-log shape clarity
// for AI-driven writes.

func init() {
	Register(Default, Tool[searchKnowledgeArgs]{
		Name: "search_knowledge",
		Description: "Search workspace knowledge pages by free-text query. Returns " +
			"permission-filtered snippets with title, heading path, and URL. " +
			"Call this before answering questions about internal docs, procedures, " +
			"policies, or project-specific knowledge.",
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
		Description: "Fetch a single workspace knowledge page by id. Returns the title, Markdown content, and excerpt if the caller can view it.",
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
		Description: "List workspace knowledge pages (optionally scoped to a parent). Returns only pages the caller can view.",
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
				pages = append(pages, pageDTO{
					ID:          p.ID,
					WorkspaceID: p.WorkspaceID,
					ParentID:    p.ParentID,
					Title:       p.Title,
					Slug:        p.Slug,
					Path:        p.Path,
					Depth:       p.Depth,
					URL:         fmt.Sprintf("/workspaces/%d/pages/%d", p.WorkspaceID, p.ID),
				})
			}
			return listPagesOut{Pages: pages}, nil
		},
	})
}
