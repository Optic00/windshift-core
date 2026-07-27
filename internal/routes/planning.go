package routes

import (
	"net/http"

	"windshift/internal/models"
)

// RegisterPlanningRoutes configures planning routes.
func RegisterPlanningRoutes(deps *Deps) {
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth
	workspaceItemEdit := deps.PermissionMiddleware.RequireWorkspacePermission(models.PermissionItemEdit)
	globalMilestoneManage := deps.PermissionMiddleware.RequireGlobalPermission(models.PermissionMilestoneCreate)
	globalIterationManage := deps.PermissionMiddleware.RequireGlobalPermission(models.PermissionIterationManage)

	api.HandleH("GET /milestone-categories", auth(http.HandlerFunc(deps.Planning.MilestoneCategory.GetAll)))
	api.HandleH("POST /milestone-categories", auth(globalMilestoneManage(http.HandlerFunc(deps.Planning.MilestoneCategory.Create))))
	api.HandleH("GET /milestone-categories/{id}", auth(http.HandlerFunc(deps.Planning.MilestoneCategory.Get)))
	api.HandleH("PUT /milestone-categories/{id}", auth(globalMilestoneManage(http.HandlerFunc(deps.Planning.MilestoneCategory.Update))))
	api.HandleH("DELETE /milestone-categories/{id}", auth(globalMilestoneManage(http.HandlerFunc(deps.Planning.MilestoneCategory.Delete))))

	api.HandleH("GET /milestones", auth(http.HandlerFunc(deps.Planning.Milestone.GetAll)))
	api.HandleH("POST /milestones", auth(http.HandlerFunc(deps.Planning.Milestone.Create)))
	api.HandleH("GET /milestones/test-statistics", auth(http.HandlerFunc(deps.Planning.Milestone.GetTestStatisticsBatch)))
	api.HandleH("GET /milestones/{id}", auth(http.HandlerFunc(deps.Planning.Milestone.Get)))
	// URL scope selects both authorization and rows eligible for update.
	api.HandleH("PUT /workspaces/{workspaceId}/milestones/{id}", auth(workspaceItemEdit(http.HandlerFunc(deps.Planning.Milestone.Update))))
	api.HandleH("PUT /global/milestones/{id}", auth(globalMilestoneManage(http.HandlerFunc(deps.Planning.Milestone.Update))))
	// Scope-specific authorization prevents cross-scope reordering.
	api.HandleH("POST /workspaces/{workspaceId}/milestones/reorder", auth(workspaceItemEdit(http.HandlerFunc(deps.Planning.Milestone.Reorder))))
	api.HandleH("POST /global/milestones/reorder", auth(globalMilestoneManage(http.HandlerFunc(deps.Planning.Milestone.Reorder))))
	api.HandleH("DELETE /milestones/{id}", auth(http.HandlerFunc(deps.Planning.Milestone.Delete)))
	api.HandleH("GET /milestones/{id}/test-statistics", auth(http.HandlerFunc(deps.Planning.Milestone.GetTestStatistics)))
	api.HandleH("GET /milestones/{id}/progress", auth(http.HandlerFunc(deps.Planning.Milestone.GetProgress)))
	api.HandleH("POST /milestones/{id}/release", auth(http.HandlerFunc(deps.Planning.Milestone.Release)))

	api.HandleH("GET /iteration-types", auth(http.HandlerFunc(deps.Planning.IterationType.GetAll)))
	api.HandleH("POST /iteration-types", auth(globalIterationManage(http.HandlerFunc(deps.Planning.IterationType.Create))))
	api.HandleH("GET /iteration-types/{id}", auth(http.HandlerFunc(deps.Planning.IterationType.Get)))
	api.HandleH("PUT /iteration-types/{id}", auth(globalIterationManage(http.HandlerFunc(deps.Planning.IterationType.Update))))
	api.HandleH("DELETE /iteration-types/{id}", auth(globalIterationManage(http.HandlerFunc(deps.Planning.IterationType.Delete))))

	api.HandleH("GET /iterations", auth(http.HandlerFunc(deps.Planning.Iteration.GetAll)))
	api.HandleH("POST /iterations", auth(http.HandlerFunc(deps.Planning.Iteration.Create)))
	api.HandleH("GET /iterations/{id}", auth(http.HandlerFunc(deps.Planning.Iteration.Get)))
	api.HandleH("POST /iterations/{id}/complete", auth(http.HandlerFunc(deps.Items.Item.CompleteIteration)))
	api.HandleH("PUT /workspaces/{workspaceId}/iterations/{id}", auth(workspaceItemEdit(http.HandlerFunc(deps.Planning.Iteration.Update))))
	api.HandleH("PUT /global/iterations/{id}", auth(globalIterationManage(http.HandlerFunc(deps.Planning.Iteration.Update))))
	api.HandleH("DELETE /iterations/{id}", auth(http.HandlerFunc(deps.Planning.Iteration.Delete)))
	// Register the bulk literal before iteration-ID wildcards.
	api.HandleH("GET /iterations/progress", auth(http.HandlerFunc(deps.Planning.Iteration.GetProgressBatch)))
	api.HandleH("GET /iterations/{id}/progress", auth(http.HandlerFunc(deps.Planning.Iteration.GetProgress)))
	api.HandleH("GET /iterations/{id}/burndown", auth(http.HandlerFunc(deps.Planning.Iteration.GetBurndown)))

	api.HandleH("GET /personal-labels", auth(http.HandlerFunc(deps.Planning.PersonalLabel.GetAll)))
	api.HandleH("POST /personal-labels", auth(http.HandlerFunc(deps.Planning.PersonalLabel.Create)))
	api.HandleH("GET /personal-labels/{id}", auth(http.HandlerFunc(deps.Planning.PersonalLabel.Get)))
	api.HandleH("PUT /personal-labels/{id}", auth(http.HandlerFunc(deps.Planning.PersonalLabel.Update)))
	api.HandleH("DELETE /personal-labels/{id}", auth(http.HandlerFunc(deps.Planning.PersonalLabel.Delete)))

	api.HandleH("GET /items/{id}/personal-labels", auth(http.HandlerFunc(deps.Planning.PersonalLabel.GetItemPersonalLabels)))
	api.HandleH("PUT /items/{id}/personal-labels", auth(http.HandlerFunc(deps.Planning.PersonalLabel.SetItemPersonalLabels)))
	api.HandleH("POST /items/{id}/personal-labels", auth(http.HandlerFunc(deps.Planning.PersonalLabel.AddItemPersonalLabel)))
	api.HandleH("DELETE /items/{id}/personal-labels/{labelId}", auth(http.HandlerFunc(deps.Planning.PersonalLabel.RemoveItemPersonalLabel)))
}
