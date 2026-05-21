package services

import (
	"fmt"

	"windshift/internal/database"
	"windshift/internal/repository"
)

// KnowledgeSource identifies where a search hit came from. Phase 1 only
// emits "page"; logbook integration will register additional sources
// without changing the public Result shape.
const (
	KnowledgeSourcePage    = "page"
	KnowledgeSourceLogbook = "logbook"
)

// KnowledgeResult is a single permission-filtered search hit.
type KnowledgeResult struct {
	Source      string  `json:"source"`
	PageID      int     `json:"page_id,omitempty"`
	ChunkID     int     `json:"chunk_id,omitempty"`
	WorkspaceID int     `json:"workspace_id"`
	Title       string  `json:"title"`
	HeadingPath string  `json:"heading_path,omitempty"`
	URL         string  `json:"url,omitempty"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
}

// KnowledgeRetrievalService is a thin facade over per-source repositories
// that always runs the appropriate permission check before returning a
// snippet. Adapters (HTTP search endpoint, AI tools) call into this
// service rather than reaching into the page repository directly so the
// re-check on read can never be skipped.
type KnowledgeRetrievalService struct {
	pages    *repository.PageRepository
	pageAuth *PagePermissionService
}

// NewKnowledgeRetrievalService wires the retrieval facade against the
// existing repository + permission evaluator.
func NewKnowledgeRetrievalService(db database.Database, pageAuth *PagePermissionService) *KnowledgeRetrievalService {
	return &KnowledgeRetrievalService{
		pages:    repository.NewPageRepository(db),
		pageAuth: pageAuth,
	}
}

// SearchInput is the request shape for unified search.
type SearchInput struct {
	UserID      int
	WorkspaceID int
	Query       string
	Limit       int
	Sources     []string // optional whitelist; empty = "all enabled"
}

// Search runs full-text search over the requested sources, filters every
// hit by the relevant per-source permission, and returns ranked results.
func (s *KnowledgeRetrievalService) Search(in SearchInput) ([]KnowledgeResult, error) {
	if in.Query == "" {
		return nil, nil
	}
	if in.Limit <= 0 || in.Limit > 100 {
		in.Limit = 25
	}
	useSource := func(name string) bool {
		if len(in.Sources) == 0 {
			return true
		}
		for _, s := range in.Sources {
			if s == name {
				return true
			}
		}
		return false
	}

	var out []KnowledgeResult
	if useSource(KnowledgeSourcePage) {
		pageHits, err := s.searchPages(in)
		if err != nil {
			return nil, err
		}
		out = append(out, pageHits...)
	}
	// Logbook integration intentionally deferred — the bucket-access model
	// lives outside PagePermissionService and is a separate slice. Until
	// then the source whitelist still accepts "logbook" to keep clients
	// forward-compatible; it just returns nothing.
	return out, nil
}

func (s *KnowledgeRetrievalService) searchPages(in SearchInput) ([]KnowledgeResult, error) {
	// Over-fetch a bit so the post-permission filter doesn't starve the
	// response when several leading hits are inaccessible.
	rawLimit := in.Limit * 3
	hits, err := s.pages.SearchChunks(in.WorkspaceID, in.Query, rawLimit)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	// Re-check permission per result. The permission evaluator is cheap
	// for the open-page case (which is what the workspace-tree default
	// produces) and prevents content from a restricted page surfacing
	// through search even if a hostile query somehow matched a hidden
	// chunk.
	out := make([]KnowledgeResult, 0, len(hits))
	cache := make(map[int]bool, len(hits))
	for _, hit := range hits {
		can, ok := cache[hit.PageID]
		if !ok {
			c, err := s.pageAuth.Can(in.UserID, in.WorkspaceID, hit.PageID, PageOpView)
			if err != nil {
				return nil, fmt.Errorf("permission check for page %d: %w", hit.PageID, err)
			}
			can = c
			cache[hit.PageID] = c
		}
		if !can {
			continue
		}
		page, err := s.pages.GetByID(hit.PageID)
		if err != nil {
			return nil, err
		}
		out = append(out, KnowledgeResult{
			Source:      KnowledgeSourcePage,
			PageID:      hit.PageID,
			ChunkID:     hit.ChunkID,
			WorkspaceID: hit.WorkspaceID,
			Title:       page.Title,
			HeadingPath: hit.HeadingPath,
			URL:         fmt.Sprintf("/workspaces/%d/pages/%d", hit.WorkspaceID, hit.PageID),
			Snippet:     hit.Snippet,
			Score:       hit.Score,
		})
		if len(out) >= in.Limit {
			break
		}
	}
	return out, nil
}
