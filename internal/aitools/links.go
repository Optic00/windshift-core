package aitools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"windshift/internal/models"
	"windshift/internal/services"
)

// Link tools — the AI-agent surface over the polymorphic item_links table
// (item ↔ item, item ↔ page, item ↔ test_case). All orchestration
// (permission gating, cross-workspace page invariant, duplicate detection,
// per-page ACL filtering) lives in services.ItemLinkService; this file is a
// thin adapter that mirrors the HTTP handlers' behavior:
//   - missing entity / no permission / cross-workspace page all collapse to
//     a generic not-found JSON error (existence-leak policy),
//   - listing drops links whose endpoints the caller cannot see,
//   - creating requires edit on the source and view on the target (page
//     endpoints additionally go through per-page ACLs),
//   - deleting requires edit on the link's source entity.
//
// The link type is auto-picked per entity-type pair when omitted, matching
// the ws CLI: Page for item↔page, Tests for item↔test_case, Relates To for
// item↔item (override with Implements / Depends On / Links To / Duplicates /
// Child Of). The server-side service re-validates pair legality regardless.

// --- arg types ---

type listLinkTypesArgs struct{}

type listLinksArgs struct {
	EntityType string `json:"entity_type,omitempty" jsonschema:"Entity type: item, page, or test_case. Defaults to item."`
	EntityID   int    `json:"entity_id,omitempty" jsonschema:"Numeric entity ID"`
	ItemKey    string `json:"item_key,omitempty" jsonschema:"Item key like PROJ-42 (items only; alternative to entity_id)"`
}

type addLinkArgs struct {
	SourceType    string `json:"source_type,omitempty" jsonschema:"Source entity type: item, page, or test_case. Defaults to item."`
	SourceID      int    `json:"source_id,omitempty" jsonschema:"Source entity numeric ID"`
	SourceItemKey string `json:"source_item_key,omitempty" jsonschema:"Source item key like PROJ-42 (items only; alternative to source_id)"`
	TargetType    string `json:"target_type,omitempty" jsonschema:"Target entity type: item, page, or test_case. Defaults to item."`
	TargetID      int    `json:"target_id,omitempty" jsonschema:"Target entity numeric ID"`
	TargetItemKey string `json:"target_item_key,omitempty" jsonschema:"Target item key like PROJ-42 (items only; alternative to target_id)"`
	LinkType      string `json:"link_type,omitempty" jsonschema:"Link type name (e.g. Implements, Depends On). Omit to auto-pick: Page for item-page, Tests for item-test_case, Relates To for item-item."`
}

type removeLinkArgs struct {
	LinkID int `json:"link_id" jsonschema:"Link ID to delete (from list_links output)"`
}

// --- output shapes ---

type linkTypeDTO struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ForwardLabel string `json:"forward_label"`
	ReverseLabel string `json:"reverse_label"`
	// AllowedEntityTypes is the unordered multiset of entity-type slots the
	// pair must fit into (e.g. ["item","page"] allows item↔page only).
	// Empty means any pair of item/page/test_case is allowed.
	AllowedEntityTypes []string `json:"allowed_entity_types,omitempty"`
}

type listLinkTypesOut struct {
	LinkTypes []linkTypeDTO `json:"link_types"`
}

type linkEndpointDTO struct {
	Type        string `json:"type"` // item | page | test_case | asset
	ID          int    `json:"id"`
	Key         string `json:"key,omitempty"` // user-facing item key, e.g. WI-42
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"` // items only
	WorkspaceID *int   `json:"workspace_id,omitempty"`
}

type linkDTO struct {
	ID           int             `json:"id"`
	LinkType     string          `json:"link_type"`
	ForwardLabel string          `json:"forward_label,omitempty"`
	ReverseLabel string          `json:"reverse_label,omitempty"`
	Source       linkEndpointDTO `json:"source"`
	Target       linkEndpointDTO `json:"target"`
	CreatedBy    string          `json:"created_by,omitempty"`
}

type listLinksOut struct {
	Outgoing []linkDTO `json:"outgoing"`
	Incoming []linkDTO `json:"incoming"`
}

// --- registry hookup ---

func init() {
	Register(Default, Tool[listLinkTypesArgs]{
		Name: "list_link_types",
		Description: "List the active link-type catalog with each type's entity-pair " +
			"constraints. allowed_entity_types is a slot multiset: [item,page] permits " +
			"item-page links only, [item,item] permits item-item, empty permits any pair. " +
			"Use the name with add_link's link_type argument.",
		Run: func(_ context.Context, env *Env, _ listLinkTypesArgs) (any, error) {
			types, err := newLinkToolService(env).ListLinkTypes(false)
			if err != nil {
				return nil, err
			}
			out := listLinkTypesOut{LinkTypes: make([]linkTypeDTO, 0, len(types))}
			for _, lt := range types {
				out.LinkTypes = append(out.LinkTypes, linkTypeDTO{
					ID:                 lt.ID,
					Name:               lt.Name,
					Description:        lt.Description,
					ForwardLabel:       lt.ForwardLabel,
					ReverseLabel:       lt.ReverseLabel,
					AllowedEntityTypes: lt.AllowedEntityTypes,
				})
			}
			return out, nil
		},
	})

	Register(Default, Tool[listLinksArgs]{
		Name: "list_links",
		Description: "List outgoing and incoming cross-entity links anchored on a work item, " +
			"page, or test case. Identify the entity by entity_type + entity_id, or by " +
			"item_key alone for work items. Links to entities the caller cannot view are omitted.",
		Run: func(_ context.Context, env *Env, args listLinksArgs) (any, error) {
			svc := newLinkToolService(env)
			entityType, entityID, errMsg := resolveLinkEntity(env, svc, args.EntityType, args.EntityID, args.ItemKey)
			if errMsg != "" {
				return map[string]string{"error": errMsg}, nil
			}
			outgoing, incoming, err := svc.ListLinksForEntityWithChecks(env.UserID, entityType, entityID)
			if err != nil {
				return linkServiceErrorResult(err)
			}
			out := listLinksOut{Outgoing: make([]linkDTO, 0, len(outgoing)), Incoming: make([]linkDTO, 0, len(incoming))}
			for i := range outgoing {
				out.Outgoing = append(out.Outgoing, linkToDTO(&outgoing[i]))
			}
			for i := range incoming {
				out.Incoming = append(out.Incoming, linkToDTO(&incoming[i]))
			}
			return out, nil
		},
	})

	Register(Default, Tool[addLinkArgs]{
		Name: "add_link",
		Description: "Create a cross-entity link (item-item, item-page, or item-test_case). " +
			"Identify each side by type + id, or by source_item_key / target_item_key for work " +
			"items. link_type is auto-picked when omitted (Page for item-page, Tests for " +
			"item-test_case, Relates To for item-item); item-item links accept Implements, " +
			"Depends On, Links To, Duplicates, or Child Of as overrides. Requires edit access " +
			"on the source and view access on the target.",
		Run: func(_ context.Context, env *Env, args addLinkArgs) (any, error) {
			svc := newLinkToolService(env)
			sourceType, sourceID, errMsg := resolveLinkEntity(env, svc, args.SourceType, args.SourceID, args.SourceItemKey)
			if errMsg != "" {
				return map[string]string{"error": "source: " + errMsg}, nil
			}
			targetType, targetID, errMsg := resolveLinkEntity(env, svc, args.TargetType, args.TargetID, args.TargetItemKey)
			if errMsg != "" {
				return map[string]string{"error": "target: " + errMsg}, nil
			}
			if sourceType == targetType && sourceID == targetID {
				return map[string]string{"error": "cannot link an entity to itself"}, nil
			}

			linkType, errMsg, err := resolveToolLinkType(svc, args.LinkType, sourceType, targetType)
			if err != nil {
				return nil, err
			}
			if errMsg != "" {
				return map[string]string{"error": errMsg}, nil
			}

			link, err := svc.CreateLinkWithChecks(env.UserID, services.CreateItemLinkParams{
				LinkTypeID: linkType.ID,
				SourceType: sourceType,
				SourceID:   sourceID,
				TargetType: targetType,
				TargetID:   targetID,
			})
			if err != nil {
				return linkServiceErrorResult(err)
			}
			return map[string]any{"link": linkToDTO(link)}, nil
		},
	})

	Register(Default, Tool[removeLinkArgs]{
		Name: "remove_link",
		Description: "Delete a cross-entity link by its numeric ID (from list_links output). " +
			"Requires edit access on the link's source entity.",
		Run: func(_ context.Context, env *Env, args removeLinkArgs) (any, error) {
			if err := newLinkToolService(env).DeleteLinkWithChecks(env.UserID, args.LinkID); err != nil {
				return linkServiceErrorResult(err)
			}
			return map[string]any{"deleted": true, "link_id": args.LinkID}, nil
		},
	})
}

// --- helpers ---

// newLinkToolService builds the orchestration-grade ItemLinkService for one
// tool call: workspace permissions + per-page ACLs are wired so create /
// delete / list run the same checks as the HTTP handlers. The asset checker
// stays nil (asset endpoints are out of scope for AI tools and fail closed),
// and the notification emitter has no Env slot, so linked/unlinked
// notifications are skipped — matching the orchestration's nil-safe design.
func newLinkToolService(env *Env) *services.ItemLinkService {
	svc := services.NewItemLinkService(env.DB).
		WithPermissionService(env.PermService).
		WithPagePermissionChecker(services.NewPagePermissionService(env.DB, env.PermService))
	if env.ActionService != nil {
		svc = svc.WithActionEmitter(env.ActionService)
	}
	return svc
}

// resolveLinkEntity normalizes an (entity_type, entity_id, item_key) triple
// into a canonical (entityType, entityID) pair and applies the Env-level
// workspace gate. Returns a non-empty errMsg (for the JSON-value error
// convention) when the reference is invalid or the entity is not accessible;
// both "missing" and "no access" share the same generic message.
func resolveLinkEntity(env *Env, svc *services.ItemLinkService, entityType string, entityID int, itemKey string) (string, int, string) {
	canonical := "item"
	if strings.TrimSpace(entityType) != "" {
		c, ok := services.CanonicalEntityType(entityType)
		if !ok || c == "asset" {
			return "", 0, fmt.Sprintf("invalid entity type %q (want item, page, or test_case)", entityType)
		}
		canonical = c
	}

	if itemKey != "" {
		if canonical != "item" {
			return "", 0, "item_key is only valid for item entities"
		}
		id, err := resolveItemID(env.DB, entityID, itemKey)
		if err != nil {
			return "", 0, err.Error()
		}
		entityID = id
	}
	if entityID <= 0 {
		return "", 0, "entity id is required (or item_key for work items)"
	}

	// Env-level workspace gate (the canonical aitools permission contract);
	// the service re-checks workspace permissions and per-page ACLs after
	// this. Missing and inaccessible collapse into one generic message.
	wsID, _, found, err := svc.ResolveEntityScope(canonical, entityID)
	if err != nil || !found || !env.HasWorkspaceAccess(wsID) {
		return "", 0, canonical + " not found"
	}
	return canonical, entityID, ""
}

// autoLinkTypeNameForPair picks the canonical link type for an entity-type
// pair when the caller didn't specify one. Mirrors the ws CLI: Page for
// item↔page, Tests for item↔test_case, Relates To for item↔item. Empty
// means no sensible default exists and the caller must pass link_type.
func autoLinkTypeNameForPair(srcType, tgtType string) string {
	a, b := srcType, tgtType
	if a > b {
		a, b = b, a
	}
	switch {
	case a == "item" && b == "page":
		return "Page"
	case a == "item" && b == "test_case":
		return "Tests"
	case a == "item" && b == "item":
		return "Relates To"
	default:
		return ""
	}
}

// linkTypeAllowsPair mirrors the server-side budget check in
// ItemLinkService.CreateLink: each endpoint consumes one slot from
// allowed_entity_types; nil/empty means any combination. Kept here as a
// client-side preflight so the tool can return a message that names the
// allowed entity types — the service remains authoritative.
func linkTypeAllowsPair(lt *models.LinkType, srcType, tgtType string) bool {
	if len(lt.AllowedEntityTypes) == 0 {
		return true
	}
	budget := make(map[string]int, len(lt.AllowedEntityTypes))
	for _, t := range lt.AllowedEntityTypes {
		budget[t]++
	}
	need := map[string]int{srcType: 1}
	need[tgtType]++
	for t, n := range need {
		if budget[t] < n {
			return false
		}
	}
	return true
}

// resolveToolLinkType returns the active LinkType for this create, or a
// user-facing errMsg when no type fits (unknown name, incompatible pair,
// or no default for the pair). The third return is a real infrastructure
// error (DB failure listing the catalog).
func resolveToolLinkType(svc *services.ItemLinkService, name, srcType, tgtType string) (*models.LinkType, string, error) {
	types, err := svc.ListLinkTypes(false)
	if err != nil {
		return nil, "", err
	}

	name = strings.TrimSpace(name)
	want := name
	if want == "" {
		want = autoLinkTypeNameForPair(srcType, tgtType)
		if want == "" {
			return nil, fmt.Sprintf("no default link type for %s-%s; pass link_type (see list_link_types)", srcType, tgtType), nil
		}
	}

	for i := range types {
		lt := &types[i]
		if !lt.Active || !strings.EqualFold(lt.Name, want) {
			continue
		}
		if !linkTypeAllowsPair(lt, srcType, tgtType) {
			allowed := "any"
			if len(lt.AllowedEntityTypes) > 0 {
				allowed = strings.Join(lt.AllowedEntityTypes, ", ")
			}
			return nil, fmt.Sprintf("link type %q does not allow %s-%s links (allowed entity types: %s)", lt.Name, srcType, tgtType, allowed), nil
		}
		return lt, "", nil
	}

	if name == "" {
		return nil, fmt.Sprintf("default link type %q not found; pass link_type (see list_link_types)", want), nil
	}
	return nil, fmt.Sprintf("link type %q not found (see list_link_types)", name), nil
}

// linkServiceErrorResult maps ItemLinkService's typed errors onto the
// aitools JSON-value error convention. Not-found, no-permission, and the
// cross-workspace page invariant all collapse into one generic message —
// the same existence-leak policy the HTTP handlers apply via 404.
func linkServiceErrorResult(err error) (any, error) {
	switch {
	case errors.Is(err, services.ErrLinkSelfReference),
		errors.Is(err, services.ErrLinkInvalidEntityType),
		errors.Is(err, services.ErrInvalidLinkTypeForEntities):
		return map[string]string{"error": err.Error()}, nil
	case errors.Is(err, services.ErrLinkExists):
		return map[string]string{"error": "a link between these entities already exists"}, nil
	case errors.Is(err, services.ErrLinkNotFound):
		return map[string]string{"error": "link not found"}, nil
	case errors.Is(err, services.ErrLinkCrossWorkspacePage),
		services.IsEntityNotAccessible(err):
		return map[string]string{"error": "entity not found"}, nil
	default:
		return nil, err
	}
}

// linkToDTO trims the wide joined models.ItemLink row down to what an agent
// needs: link id + type labels and resolved display info for both ends
// (item key/title/status, page title, test-case title).
func linkToDTO(l *models.ItemLink) linkDTO {
	return linkDTO{
		ID:           l.ID,
		LinkType:     l.LinkTypeName,
		ForwardLabel: l.LinkTypeForwardLabel,
		ReverseLabel: l.LinkTypeReverseLabel,
		Source:       linkEndpointDTOFrom(l.SourceType, l.SourceID, l.SourceWorkspaceKey, l.SourceItemNumber, l.SourceTitle, l.SourceStatusName, l.SourceWorkspaceID),
		Target:       linkEndpointDTOFrom(l.TargetType, l.TargetID, l.TargetWorkspaceKey, l.TargetItemNumber, l.TargetTitle, l.TargetStatusName, l.TargetWorkspaceID),
		CreatedBy:    l.CreatedByName,
	}
}

// linkEndpointDTOFrom builds one side of a linkDTO. The user-facing item key
// is workspace key + workspace_item_number (never the global DB id — see the
// SourceItemNumber doc comment on models.ItemLink).
func linkEndpointDTOFrom(entityType string, id int, wsKey string, itemNumber *int, title, status string, wsID *int) linkEndpointDTO {
	ep := linkEndpointDTO{Type: entityType, ID: id, Title: title, WorkspaceID: wsID}
	if entityType == "item" {
		ep.Status = status
		if wsKey != "" && itemNumber != nil {
			ep.Key = fmt.Sprintf("%s-%d", wsKey, *itemNumber)
		}
	}
	return ep
}
