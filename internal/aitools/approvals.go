package aitools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"windshift/internal/auth"
	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

type getItemApprovalsArgs struct {
	ItemID  int    `json:"item_id,omitempty" jsonschema:"The item numeric ID"`
	ItemKey string `json:"item_key,omitempty" jsonschema:"The item key like PROJ-42"`
}

type approvalStepOut struct {
	Order     int      `json:"order"`
	Status    string   `json:"status"`
	Approvers []string `json:"approvers,omitempty"`
	StartedAt string   `json:"started_at,omitempty"`
}

type approvalDecisionOut struct {
	Decision    string `json:"decision"`
	By          string `json:"by,omitempty"`
	Comment     string `json:"comment,omitempty"`
	DelegatedTo string `json:"delegated_to,omitempty"`
	At          string `json:"at"`
}

type approvalRequestOut struct {
	ID          int                   `json:"id"`
	Status      string                `json:"status"`
	TriggeredBy string                `json:"triggered_by,omitempty"`
	OpenedAt    string                `json:"opened_at"`
	CompletedAt string                `json:"completed_at,omitempty"`
	Steps       []approvalStepOut     `json:"steps,omitempty"`
	Decisions   []approvalDecisionOut `json:"decisions,omitempty"`
}

type getItemApprovalsOut struct {
	Requests []approvalRequestOut `json:"requests"`
}

func init() {
	Register(Default, Tool[getItemApprovalsArgs]{
		Name:        "get_item_approvals",
		Group:       CapabilityUsersApprovals,
		Access:      AccessRead,
		Risk:        RiskLow,
		Description: "Get the approval state and history for a work item: the current pending approval request (if any), step status, who can approve, and the full audit trail of approve/reject/comment/cancel decisions with their comments.",
		Scopes:      []string{auth.ScopeItemsRead}, // item-scoped content; no dedicated v1 route, gate like other item reads
		Run: func(ctx context.Context, env *Env, args getItemApprovalsArgs) (any, error) {
			itemID, err := resolveItemID(env.DB, args.ItemID, args.ItemKey)
			if err != nil {
				return map[string]string{"error": err.Error()}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}

			wsID, err := services.NewItemCRUDService(env.DB).GetWorkspaceID(itemID)
			if err != nil {
				return map[string]string{"error": "item not found"}, nil //nolint:nilerr // surface as a tool error in JSON, not as a protocol error
			}
			if !env.HasWorkspaceAccess(wsID) {
				return map[string]string{"error": "item not found"}, nil
			}

			repo := repository.NewApprovalRepository(env.DB)
			ids, err := repo.FindRequestIDsForItem(ctx, itemID)
			if err != nil {
				return nil, fmt.Errorf("load approval requests: %w", err)
			}

			userIDs := map[int]struct{}{}
			requests := make([]*models.ApprovalRequest, 0, len(ids))
			for _, id := range ids {
				req, err := repo.FindFullRequestByID(ctx, id)
				if err != nil {
					continue
				}
				requests = append(requests, req)
				userIDs[req.TriggeredByUserID] = struct{}{}
				for _, si := range req.StepInstances {
					for _, ap := range si.Approvers {
						if ap.UserID != nil {
							userIDs[*ap.UserID] = struct{}{}
						}
					}
				}
				for _, d := range req.Decisions {
					if d.ActorUserID != nil {
						userIDs[*d.ActorUserID] = struct{}{}
					}
					if d.DelegatedToUserID != nil {
						userIDs[*d.DelegatedToUserID] = struct{}{}
					}
				}
			}
			names := services.NewIDResolverService(env.DB).ResolveUserNames(userIDs)
			resolveName := func(id *int) string {
				if id == nil {
					return ""
				}
				if n, ok := names[*id]; ok {
					return n
				}
				return fmt.Sprintf("user #%d", *id)
			}

			out := getItemApprovalsOut{Requests: make([]approvalRequestOut, 0, len(requests))}
			for _, req := range requests {
				ro := approvalRequestOut{
					ID:          req.ID,
					Status:      req.Status,
					TriggeredBy: resolveName(&req.TriggeredByUserID),
					OpenedAt:    req.CreatedAt.Format(time.RFC3339),
				}
				if req.CompletedAt != nil {
					ro.CompletedAt = req.CompletedAt.Format(time.RFC3339)
				}
				for _, si := range req.StepInstances {
					s := approvalStepOut{Order: si.DisplayOrder + 1, Status: si.Status}
					if si.StartedAt != nil {
						s.StartedAt = si.StartedAt.Format(time.RFC3339)
					}
					for _, ap := range si.Approvers {
						if !ap.IsActive || ap.UserID == nil {
							continue
						}
						s.Approvers = append(s.Approvers, resolveName(ap.UserID))
					}
					ro.Steps = append(ro.Steps, s)
				}
				for _, d := range req.Decisions {
					ro.Decisions = append(ro.Decisions, approvalDecisionOut{
						Decision:    d.Decision,
						By:          resolveName(d.ActorUserID),
						Comment:     d.Comment,
						DelegatedTo: resolveName(d.DelegatedToUserID),
						At:          d.CreatedAt.Format(time.RFC3339),
					})
				}
				out.Requests = append(out.Requests, ro)
			}
			return out, nil
		},
	})
}

// resolveItemID accepts either a numeric ID or a workspace-key+number pair
// (e.g. "FW-10") and returns the underlying item ID.
func resolveItemID(db database.Database, itemID int, itemKey string) (int, error) {
	if itemID > 0 {
		return itemID, nil
	}
	if itemKey == "" {
		return 0, errors.New("must provide item_id or item_key")
	}
	parts := strings.SplitN(strings.ToUpper(itemKey), "-", 2)
	if len(parts) != 2 {
		return 0, errors.New("invalid item key format, expected KEY-NUMBER")
	}
	num, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, errors.New("invalid item key format, expected KEY-NUMBER")
	}
	id, err := repository.NewItemRepository(db).FindIDByKeyAndNumber(parts[0], num)
	if err != nil {
		return 0, errors.New("item not found")
	}
	return id, nil
}
