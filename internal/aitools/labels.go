package aitools

import (
	"context"
	"errors"
	"fmt"

	"windshift/internal/auth"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

type listLabelsArgs struct {
	WorkspaceID int `json:"workspace_id" jsonschema:"Workspace ID used to authorize access to the global label catalog"`
}

type labelDTO struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type listLabelsOut struct {
	Labels []labelDTO `json:"labels"`
}

type setItemLabelsArgs struct {
	ItemID   int    `json:"item_id,omitempty" jsonschema:"Item ID to set labels on. Provide either this or item_key."`
	ItemKey  string `json:"item_key,omitempty" jsonschema:"Item key like PROJ-42. Provide either this or item_id."`
	LabelIDs []int  `json:"label_ids" jsonschema:"Label IDs to set (replaces all existing labels)"`
}

type setItemLabelsOut struct {
	ItemID   int   `json:"item_id"`
	LabelIDs []int `json:"label_ids"`
	Updated  bool  `json:"updated"`
}

func init() {
	Register(Default, Tool[listLabelsArgs]{
		Name:        "list_labels",
		Group:       CapabilityPlanningActivity,
		Access:      AccessRead,
		Risk:        RiskLow,
		Description: "List the global work-item label catalog.",
		Scopes:      []string{auth.ScopeItemsRead},
		Run: func(_ context.Context, env *Env, args listLabelsArgs) (any, error) {
			if !env.HasWorkspaceAccess(args.WorkspaceID) {
				return map[string]string{"error": "workspace not found"}, nil
			}
			labels, err := repository.NewLabelRepository(env.DB).ListAll()
			if err != nil {
				return nil, err
			}
			out := listLabelsOut{Labels: make([]labelDTO, 0, len(labels))}
			for _, l := range labels {
				out.Labels = append(out.Labels, labelDTO{ID: l.ID, Name: l.Name, Color: l.Color})
			}
			return out, nil
		},
	})

	Register(Default, Tool[setItemLabelsArgs]{
		Name:        "set_item_labels",
		Group:       CapabilityPlanningActivity,
		Access:      AccessWrite,
		Risk:        RiskMedium,
		Description: "Set labels on a work item (replaces existing labels). Identifies the item by numeric ID or key (e.g. PROJ-42).",
		Scopes:      []string{auth.ScopeItemsWrite},
		Run: func(_ context.Context, env *Env, args setItemLabelsArgs) (any, error) {
			itemID, err := resolveItemID(env.DB, args.ItemID, args.ItemKey)
			if err != nil {
				return map[string]string{"error": err.Error()}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			item, err := services.NewItemCRUDService(env.DB).GetByID(itemID)
			if err != nil {
				return map[string]string{"error": "item not found"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if !env.HasWorkspaceAccess(item.WorkspaceID) {
				return map[string]string{"error": "item not found"}, nil
			}
			ok, err := env.PermService.HasWorkspacePermission(env.UserID, item.WorkspaceID, models.PermissionItemEdit)
			if err != nil {
				return nil, err
			}
			if !ok {
				return map[string]string{"error": "permission denied"}, nil
			}
			labelRepo := repository.NewLabelRepository(env.DB)
			// Validate every requested global label and deduplicate IDs.
			seen := make(map[int]bool, len(args.LabelIDs))
			labelIDs := make([]int, 0, len(args.LabelIDs))
			for _, labelID := range args.LabelIDs {
				_, err := labelRepo.GetByID(labelID)
				if errors.Is(err, repository.ErrNotFound) {
					return map[string]string{"error": fmt.Sprintf("label %d not found", labelID)}, nil
				}
				if err != nil {
					return nil, err
				}
				if seen[labelID] {
					continue
				}
				seen[labelID] = true
				labelIDs = append(labelIDs, labelID)
			}
			if err := labelRepo.ReplaceItemLabels(itemID, labelIDs); err != nil {
				return nil, err
			}
			env.AuditWrite(logger.ResourceItem, itemID, "set_item_labels", item.Title)
			return setItemLabelsOut{ItemID: itemID, LabelIDs: args.LabelIDs, Updated: true}, nil
		},
	})
}
