package services

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"windshift/internal/database"
)

// ItemLinkService handles item link creation in the database.
// HTTP concerns (notifications, action events) remain in the handler.
type ItemLinkService struct {
	db database.Database
}

// NewItemLinkService creates a new ItemLinkService.
func NewItemLinkService(db database.Database) *ItemLinkService {
	return &ItemLinkService{db: db}
}

// CreateItemLinkParams contains the parameters for creating an item link.
type CreateItemLinkParams struct {
	LinkTypeID    int
	SourceType    string
	SourceID      int
	TargetType    string
	TargetID      int
	CreatedBy     *int
	CustomFieldID *int
}

// ErrInvalidLinkTypeForEntities is returned when the requested link type
// does not allow the given source/target entity types. Centralizing this
// in the service ensures both the public REST handler and the AI
// AcceptDependencies path get the same gate — without it, AI callers
// could create item↔item Tests links that the REST endpoint rejects.
var ErrInvalidLinkTypeForEntities = errors.New("link type does not allow these entity types")

// CreateLink validates and inserts a new item link.
// Returns the new link ID, or 0 if the link was a duplicate (INSERT OR IGNORE).
func (s *ItemLinkService) CreateLink(params CreateItemLinkParams) (int64, error) {
	// Verify the link type exists and is active, and pull its
	// allowed_entity_types constraint in the same query so we can gate
	// pairs that the link type's schema disallows.
	var (
		active             bool
		allowedEntityTypes sql.NullString
	)
	err := s.db.QueryRow(
		"SELECT active, allowed_entity_types FROM link_types WHERE id = ?",
		params.LinkTypeID,
	).Scan(&active, &allowedEntityTypes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("link type %d not found", params.LinkTypeID)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to check link type: %w", err)
	}
	if !active {
		return 0, fmt.Errorf("link type %d is not active", params.LinkTypeID)
	}

	// When the link type declares an allowed_entity_types set, require the
	// unordered {source, target} multiset to be covered by it. e.g.
	// ["item","test_case"] permits (item,test_case) and its mirror but
	// rejects (item,item), because the pair needs one "item" slot and one
	// "test_case" slot. nil/empty allowed_entity_types means no
	// restriction. Mirrors the legacy hardcoded LinkTypeID==1 gate so
	// direct callers of the service (notably AcceptDependencies) can't
	// bypass it.
	if allowedEntityTypes.Valid && allowedEntityTypes.String != "" {
		var allowed []string
		if err := json.Unmarshal([]byte(allowedEntityTypes.String), &allowed); err != nil {
			return 0, fmt.Errorf("invalid allowed_entity_types on link type %d: %w", params.LinkTypeID, err)
		}
		if len(allowed) > 0 {
			budget := make(map[string]int, len(allowed))
			for _, t := range allowed {
				budget[t]++
			}
			need := map[string]int{params.SourceType: 1}
			need[params.TargetType]++
			for t, n := range need {
				if budget[t] < n {
					return 0, ErrInvalidLinkTypeForEntities
				}
			}
		}
	}

	// Insert with ON CONFLICT DO NOTHING to handle duplicates gracefully
	var linkID int64
	err = s.db.QueryRow(`
		INSERT INTO item_links (link_type_id, source_type, source_id, target_type, target_id, created_by, custom_field_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT DO NOTHING
		RETURNING id
	`, params.LinkTypeID, params.SourceType, params.SourceID, params.TargetType, params.TargetID, params.CreatedBy, params.CustomFieldID).Scan(&linkID)
	if errors.Is(err, sql.ErrNoRows) {
		// Duplicate — ON CONFLICT DO NOTHING returns no row
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to create item link: %w", err)
	}

	return linkID, nil
}
