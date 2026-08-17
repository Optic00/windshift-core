package routes

import (
	"net/http"

	"windshift/internal/models"
)

// RegisterWorkspaceRoutes registers workspace-related routes (workspaces, screens, config sets, statuses, workflows).
func RegisterWorkspaceRoutes(deps *Deps) {
	api := deps.API
	auth := deps.AuthMiddleware.RequireAuth
	admin := deps.PermissionMiddleware.RequireSystemAdmin()
	workspaceView := deps.PermissionMiddleware.RequireWorkspacePermission(models.PermissionItemView)

	// Workspace endpoints
	api.HandleH("GET /workspaces", auth(http.HandlerFunc(deps.Workspaces.Workspace.GetAll)))
	api.HandleH("POST /workspaces", auth(http.HandlerFunc(deps.Workspaces.Workspace.Create)))
	api.HandleH("GET /workspace-templates", auth(http.HandlerFunc(deps.Workspaces.Workspace.ListTemplates)))
	api.HandleH("GET /workspaces/personal", auth(http.HandlerFunc(deps.Workspaces.Workspace.GetOrCreatePersonalWorkspace)))
	api.HandleH("GET /workspaces/{id}/bootstrap", auth(http.HandlerFunc(deps.Workspaces.Bootstrap.Get)))
	api.HandleH("GET /workspaces/{id}", auth(http.HandlerFunc(deps.Workspaces.Workspace.Get)))
	api.HandleH("PUT /workspaces/{id}", auth(http.HandlerFunc(deps.Workspaces.Workspace.Update)))
	api.HandleH("DELETE /workspaces/{id}", auth(http.HandlerFunc(deps.Workspaces.Workspace.Delete)))
	api.HandleH("GET /workspaces/{id}/stats", auth(workspaceView(http.HandlerFunc(deps.Workspaces.Workspace.GetStats))))
	api.HandleH("GET /workspaces/{id}/statuses", auth(http.HandlerFunc(deps.Workspaces.Workspace.GetStatuses)))
	api.HandleH("GET /workspaces/{id}/item-types", auth(workspaceView(http.HandlerFunc(deps.Workspaces.Workspace.GetItemTypes))))
	api.HandleH("GET /workspaces/{id}/homepage/layout", auth(http.HandlerFunc(deps.Workspaces.Workspace.GetHomepageLayout)))
	api.HandleH("PUT /workspaces/{id}/homepage/layout", auth(http.HandlerFunc(deps.Workspaces.Workspace.UpdateHomepageLayout)))

	// Workspace-scoped time projects (with category restrictions)
	api.HandleH("GET /workspaces/{id}/projects", auth(workspaceView(http.HandlerFunc(deps.TimeTracking.Project.GetByWorkspace))))

	// Screen endpoints
	api.HandleH("GET /screens", auth(http.HandlerFunc(deps.Workspaces.Screen.GetAll)))
	api.HandleH("POST /screens", admin(http.HandlerFunc(deps.Workspaces.Screen.Create)))
	api.HandleH("GET /screens/{id}", auth(http.HandlerFunc(deps.Workspaces.Screen.Get)))
	api.HandleH("PUT /screens/{id}", admin(http.HandlerFunc(deps.Workspaces.Screen.Update)))
	api.HandleH("DELETE /screens/{id}", admin(http.HandlerFunc(deps.Workspaces.Screen.Delete)))
	api.HandleH("GET /screens/{id}/fields", auth(http.HandlerFunc(deps.Workspaces.Screen.GetFields)))
	api.HandleH("PUT /screens/{id}/fields", admin(http.HandlerFunc(deps.Workspaces.Screen.UpdateFields)))
	api.HandleH("PUT /screens/{id}/system-fields", admin(http.HandlerFunc(deps.Workspaces.Screen.UpdateSystemFields)))

	// Configuration Set endpoints
	api.HandleH("GET /configuration-sets", auth(http.HandlerFunc(deps.Workspaces.ConfigSet.GetAll)))
	api.HandleH("POST /configuration-sets", admin(http.HandlerFunc(deps.Workspaces.ConfigSet.Create)))
	api.HandleH("GET /configuration-sets/{id}", auth(http.HandlerFunc(deps.Workspaces.ConfigSet.Get)))
	api.HandleH("PUT /configuration-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ConfigSet.Update)))
	api.HandleH("DELETE /configuration-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ConfigSet.Delete)))
	api.HandleH("GET /configuration-sets/{id}/analyze-migration", auth(http.HandlerFunc(deps.Workspaces.ConfigSet.AnalyzeMigration)))
	api.HandleH("POST /configuration-sets/execute-migration", admin(deps.SetupLimiter.Limit(http.HandlerFunc(deps.Workspaces.ConfigSet.ExecuteMigration))))
	api.HandleH("GET /configuration-sets/{id}/analyze-comprehensive-migration", auth(http.HandlerFunc(deps.Workspaces.ConfigSet.AnalyzeComprehensiveMigration)))
	api.HandleH("POST /configuration-sets/execute-comprehensive-migration", admin(deps.SetupLimiter.Limit(http.HandlerFunc(deps.Workspaces.ConfigSet.ExecuteComprehensiveMigration))))
	api.HandleH("GET /configuration-sets/{id}/export", auth(http.HandlerFunc(deps.Workspaces.ConfigSet.Export)))
	api.HandleH("POST /configuration-sets/import", admin(deps.SetupLimiter.Limit(http.HandlerFunc(deps.Workspaces.ConfigSet.Import))))

	// Notification Settings endpoints
	api.HandleH("GET /notification-settings", auth(http.HandlerFunc(deps.Workspaces.NotificationSettings.GetNotificationSettings)))
	api.HandleH("POST /notification-settings", admin(http.HandlerFunc(deps.Workspaces.NotificationSettings.CreateNotificationSetting)))
	api.HandleH("GET /notification-settings/available-events", auth(http.HandlerFunc(deps.Workspaces.NotificationSettings.GetAvailableEvents)))
	api.HandleH("GET /notification-settings/{id}", auth(http.HandlerFunc(deps.Workspaces.NotificationSettings.GetNotificationSetting)))
	api.HandleH("PUT /notification-settings/{id}", admin(http.HandlerFunc(deps.Workspaces.NotificationSettings.UpdateNotificationSetting)))
	api.HandleH("DELETE /notification-settings/{id}", admin(http.HandlerFunc(deps.Workspaces.NotificationSettings.DeleteNotificationSetting)))

	// Configuration Set Notification assignments
	api.HandleH("GET /configuration-sets/{config_set_id}/notification-settings", auth(http.HandlerFunc(deps.Workspaces.ConfigSetNotification.GetConfigurationSetNotifications)))
	api.HandleH("POST /configuration-sets/{config_set_id}/notification-settings", admin(http.HandlerFunc(deps.Workspaces.ConfigSetNotification.AssignNotificationToConfigurationSet)))
	api.HandleH("DELETE /configuration-sets/{config_set_id}/notification-settings/{assignment_id}", admin(http.HandlerFunc(deps.Workspaces.ConfigSetNotification.UnassignNotificationFromConfigurationSet)))
	api.HandleH("GET /configuration-sets/{config_set_id}/available-notification-settings", auth(http.HandlerFunc(deps.Workspaces.ConfigSetNotification.GetAvailableNotificationSettings)))

	// Item Type endpoints
	api.HandleH("GET /item-types", auth(http.HandlerFunc(deps.Workspaces.ItemType.GetAll)))
	api.HandleH("POST /item-types", admin(http.HandlerFunc(deps.Workspaces.ItemType.Create)))
	api.HandleH("GET /item-types/{id}", auth(http.HandlerFunc(deps.Workspaces.ItemType.Get)))
	api.HandleH("PUT /item-types/{id}", admin(http.HandlerFunc(deps.Workspaces.ItemType.Update)))
	api.HandleH("DELETE /item-types/{id}", admin(http.HandlerFunc(deps.Workspaces.ItemType.Delete)))

	// Priority endpoints
	api.HandleH("GET /priorities", auth(http.HandlerFunc(deps.Workspaces.Priority.GetAll)))
	api.HandleH("POST /priorities", admin(http.HandlerFunc(deps.Workspaces.Priority.Create)))
	api.HandleH("GET /priorities/{id}", auth(http.HandlerFunc(deps.Workspaces.Priority.Get)))
	api.HandleH("PUT /priorities/{id}", admin(http.HandlerFunc(deps.Workspaces.Priority.Update)))
	api.HandleH("DELETE /priorities/{id}", admin(http.HandlerFunc(deps.Workspaces.Priority.Delete)))

	// Hierarchy Level endpoints
	api.HandleH("GET /hierarchy-levels", auth(http.HandlerFunc(deps.Workspaces.HierarchyLevel.GetAll)))
	api.HandleH("POST /hierarchy-levels", admin(http.HandlerFunc(deps.Workspaces.HierarchyLevel.Create)))
	api.HandleH("GET /hierarchy-levels/{id}", auth(http.HandlerFunc(deps.Workspaces.HierarchyLevel.Get)))
	api.HandleH("PUT /hierarchy-levels/{id}", admin(http.HandlerFunc(deps.Workspaces.HierarchyLevel.Update)))
	api.HandleH("DELETE /hierarchy-levels/{id}", admin(http.HandlerFunc(deps.Workspaces.HierarchyLevel.Delete)))

	// Status Category endpoints
	api.HandleH("GET /status-categories", auth(http.HandlerFunc(deps.Workspaces.StatusCategory.GetAll)))
	api.HandleH("POST /status-categories", admin(http.HandlerFunc(deps.Workspaces.StatusCategory.Create)))
	api.HandleH("GET /status-categories/{id}", auth(http.HandlerFunc(deps.Workspaces.StatusCategory.Get)))
	api.HandleH("PUT /status-categories/{id}", admin(http.HandlerFunc(deps.Workspaces.StatusCategory.Update)))
	api.HandleH("DELETE /status-categories/{id}", admin(http.HandlerFunc(deps.Workspaces.StatusCategory.Delete)))

	// Status endpoints
	api.HandleH("GET /statuses", auth(http.HandlerFunc(deps.Workspaces.Status.GetAll)))
	api.HandleH("POST /statuses", admin(http.HandlerFunc(deps.Workspaces.Status.Create)))
	api.HandleH("GET /statuses/non-done-ids", auth(http.HandlerFunc(deps.Workspaces.StatusQuery.GetNonDoneStatusIDs)))
	api.HandleH("GET /statuses/{id}", auth(http.HandlerFunc(deps.Workspaces.Status.Get)))
	api.HandleH("PUT /statuses/{id}", admin(http.HandlerFunc(deps.Workspaces.Status.Update)))
	api.HandleH("DELETE /statuses/{id}", admin(http.HandlerFunc(deps.Workspaces.Status.Delete)))

	// Workflow endpoints
	api.HandleH("GET /workflows", auth(http.HandlerFunc(deps.Workspaces.Workflow.GetAll)))
	api.HandleH("POST /workflows", admin(http.HandlerFunc(deps.Workspaces.Workflow.Create)))
	api.HandleH("GET /workflows/{id}", auth(http.HandlerFunc(deps.Workspaces.Workflow.Get)))
	api.HandleH("PUT /workflows/{id}", admin(http.HandlerFunc(deps.Workspaces.Workflow.Update)))
	api.HandleH("DELETE /workflows/{id}", admin(http.HandlerFunc(deps.Workspaces.Workflow.Delete)))
	api.HandleH("GET /workflows/{id}/transitions", auth(http.HandlerFunc(deps.Workspaces.Workflow.GetTransitions)))
	api.HandleH("PUT /workflows/{id}/transitions", admin(http.HandlerFunc(deps.Workspaces.Workflow.UpdateTransitions)))
	api.HandleH("GET /workflows/{id}/available-transitions/{statusId}", auth(http.HandlerFunc(deps.Workspaces.Workflow.GetAvailableTransitions)))

	// Recurrence Rules (workspace-scoped listing)
	if deps.Items.Recurrence != nil {
		api.HandleH("GET /workspaces/{id}/recurrence-rules", auth(http.HandlerFunc(deps.Items.Recurrence.ListByWorkspace)))
	}

	// Condition Set endpoints
	if deps.Workspaces.ConditionSet != nil {
		api.HandleH("GET /condition-sets", auth(http.HandlerFunc(deps.Workspaces.ConditionSet.GetAll)))
		api.HandleH("POST /condition-sets", admin(http.HandlerFunc(deps.Workspaces.ConditionSet.Create)))
		api.HandleH("GET /condition-sets/{id}", auth(http.HandlerFunc(deps.Workspaces.ConditionSet.Get)))
		api.HandleH("PUT /condition-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ConditionSet.Update)))
		api.HandleH("DELETE /condition-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ConditionSet.Delete)))
		api.HandleH("GET /workflows/{id}/condition-sets", auth(http.HandlerFunc(deps.Workspaces.ConditionSet.GetByWorkflow)))
	}

	// Per-transition governance lookup — powers the FE override-warning UI:
	// returns the condition sets and approval sets that touch a transition.
	if deps.Workspaces.TransitionGovernance != nil {
		api.HandleH("GET /transitions/{id}/governance", auth(http.HandlerFunc(deps.Workspaces.TransitionGovernance.Get)))
	}

	// Approval Set endpoints (sibling of condition sets)
	if deps.Workspaces.ApprovalSet != nil {
		api.HandleH("GET /approval-sets", auth(http.HandlerFunc(deps.Workspaces.ApprovalSet.GetAll)))
		api.HandleH("POST /approval-sets", admin(http.HandlerFunc(deps.Workspaces.ApprovalSet.Create)))
		api.HandleH("GET /approval-sets/{id}", auth(http.HandlerFunc(deps.Workspaces.ApprovalSet.Get)))
		api.HandleH("PUT /approval-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ApprovalSet.Update)))
		api.HandleH("DELETE /approval-sets/{id}", admin(http.HandlerFunc(deps.Workspaces.ApprovalSet.Delete)))
		api.HandleH("GET /workflows/{id}/approval-sets", auth(http.HandlerFunc(deps.Workspaces.ApprovalSet.GetByWorkflow)))
	}

	if deps.Workspaces.Approval != nil {
		api.HandleH("GET /items/{id}/approvals", auth(http.HandlerFunc(deps.Workspaces.Approval.GetForItem)))
		api.HandleH("GET /approvals/mine", auth(http.HandlerFunc(deps.Workspaces.Approval.MyPending)))
		api.HandleH("GET /approvals/{id}", auth(http.HandlerFunc(deps.Workspaces.Approval.Get)))
		api.HandleH("POST /approvals/{id}/decide", auth(http.HandlerFunc(deps.Workspaces.Approval.Decide)))
		api.HandleH("POST /approvals/{id}/cancel", auth(http.HandlerFunc(deps.Workspaces.Approval.Cancel)))
		api.HandleH("POST /approvals/{id}/delegate", auth(http.HandlerFunc(deps.Workspaces.Approval.Delegate)))
		api.HandleH("POST /approvals/{id}/steps/{step_id}/refresh-approvers", auth(http.HandlerFunc(deps.Workspaces.Approval.RefreshApprovers)))
		api.HandleH("POST /approvals/{id}/steps/{step_id}/escalate", auth(http.HandlerFunc(deps.Workspaces.Approval.EscalateNow)))
	}

	// Binding handlers apply workspace-admin authorization consistently.
	if deps.Workspaces.AgentBinding != nil {
		api.HandleH("GET /workspaces/{workspaceId}/agent-profiles", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Catalog)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-bindings", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.List)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-templates", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Templates)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-profiles", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.CreateProfile)))
		api.HandleH("PATCH /workspaces/{workspaceId}/agent-profiles/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.UpdateProfile)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-profiles/{id}/migrate-runner", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.MigrateLegacyProfile)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-profiles/{id}/connect-runner", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ConnectCodingRunner)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-profiles/{id}/test", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.TestProfile)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-profiles/{id}/validation", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ValidateProfile)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-profiles/{id}/ready", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ActivateProfile)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-runner-pools/{poolId}/tokens", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.MintRunnerSetupToken)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-runner-pools/{poolId}/tokens", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ListRunnerSetupTokens)))
		api.HandleH("DELETE /workspaces/{workspaceId}/agent-runner-pools/{poolId}/tokens/{tokenId}", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.RevokeRunnerSetupToken)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-runner-pools/{poolId}/instances", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ListRunnerSetupInstances)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-tool-capabilities", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.ToolCapabilities)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-bindings/standard-prompt", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.InitialPrompt)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-bindings", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Create)))
		api.HandleH("PUT /workspaces/{workspaceId}/agent-bindings/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Update)))
		api.HandleH("DELETE /workspaces/{workspaceId}/agent-bindings/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Delete)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-bindings/{id}/restore", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Restore)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-bindings/{id}/test-llm", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.TestLLM)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-bindings/{id}/test-run", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.TestRun)))
		api.HandleH("PUT /workspaces/{workspaceId}/agent-bindings/{id}/agent-config", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.UpdateAgentConfig)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-skills", auth(http.HandlerFunc(deps.Workspaces.AgentSkill.List)))
		api.HandleH("POST /workspaces/{workspaceId}/agent-skills", auth(http.HandlerFunc(deps.Workspaces.AgentSkill.Create)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-skills/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentSkill.Get)))
		api.HandleH("PUT /workspaces/{workspaceId}/agent-skills/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentSkill.Update)))
		api.HandleH("DELETE /workspaces/{workspaceId}/agent-skills/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentSkill.Delete)))
		api.HandleH("GET /workspaces/{workspaceId}/agent-binding-candidates", auth(http.HandlerFunc(deps.Workspaces.AgentBinding.Candidates)))
	}

	// Run reads require item view; cancellation requires workspace admin.
	if deps.Workspaces.AgentRun != nil {
		api.HandleH("GET /workspaces/{workspaceId}/agent-runs", auth(http.HandlerFunc(deps.Workspaces.AgentRun.List)))
		api.HandleH("GET /items/{itemId}/agent-runs", auth(http.HandlerFunc(deps.Workspaces.AgentRun.ListForItem)))
		api.HandleH("POST /items/{itemId}/agent-runs", auth(http.HandlerFunc(deps.Workspaces.AgentRun.Rerun)))
		api.HandleH("GET /agent-runs/{id}", auth(http.HandlerFunc(deps.Workspaces.AgentRun.Get)))
		api.HandleH("GET /agent-runs/{id}/usage", auth(http.HandlerFunc(deps.Workspaces.AgentRun.Usage)))
		api.HandleH("GET /agent-runs/{id}/events", auth(http.HandlerFunc(deps.Workspaces.AgentRun.Events)))
		api.HandleH("POST /agent-runs/{id}/cancel", auth(http.HandlerFunc(deps.Workspaces.AgentRun.Cancel)))
	}

	// Runner control uses inline runner credentials rather than user sessions.
	if deps.Workspaces.RunnerControl != nil {
		register := http.Handler(http.HandlerFunc(deps.Workspaces.RunnerControl.Register))
		if deps.RunnerRegisterLimiter != nil {
			register = deps.RunnerRegisterLimiter.Limit(register)
		}
		api.HandleH("POST /runner/register", register)
		api.HandleH("POST /runner/claim", http.HandlerFunc(deps.Workspaces.RunnerControl.Claim))
		api.HandleH("POST /runner/runs/{id}/events", http.HandlerFunc(deps.Workspaces.RunnerControl.Events))
		api.HandleH("POST /runner/runs/{id}/result", http.HandlerFunc(deps.Workspaces.RunnerControl.Result))
		api.HandleH("POST /runner/heartbeat", http.HandlerFunc(deps.Workspaces.RunnerControl.Heartbeat))
	}

	// Broker routes authenticate with a per-run token.
	if deps.Workspaces.RunnerBroker != nil {
		api.HandleH("GET /secrets/{run}/{credentialId}", http.HandlerFunc(deps.Workspaces.RunnerBroker.GetSecret))
		api.HandleH("POST /llm-proxy/{run}/complete", http.HandlerFunc(deps.Workspaces.RunnerBroker.ProxyLLM))
		api.HandleH("GET /git-proxy/{ws}/{owner}/{repo}/{gitpath...}", http.HandlerFunc(deps.Workspaces.RunnerBroker.ProxyGit))
		api.HandleH("POST /git-proxy/{ws}/{owner}/{repo}/{gitpath...}", http.HandlerFunc(deps.Workspaces.RunnerBroker.ProxyGit))
		api.HandleH("GET /http-proxy/{run}", http.HandlerFunc(deps.Workspaces.RunnerBroker.ProxyHTTP))
		api.HandleH("POST /http-proxy/{run}", http.HandlerFunc(deps.Workspaces.RunnerBroker.ProxyHTTP))
	}

	if deps.Workspaces.Actions != nil {
		actionManage := deps.PermissionMiddleware.RequireWorkspacePermission(models.PermissionActionManage)

		api.HandleH("GET /workspaces/{workspaceId}/action-catalog", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.GetActionCatalog))))
		api.HandleH("GET /workspaces/{workspaceId}/actions", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.ListActions))))
		api.HandleH("POST /workspaces/{workspaceId}/actions", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.CreateAction))))
		api.HandleH("GET /workspaces/{workspaceId}/actions/{id}", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.GetAction))))
		api.HandleH("PUT /workspaces/{workspaceId}/actions/{id}", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.UpdateAction))))
		api.HandleH("DELETE /workspaces/{workspaceId}/actions/{id}", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.DeleteAction))))
		api.HandleH("POST /workspaces/{workspaceId}/actions/{id}/toggle", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.ToggleAction))))
		// Execute performs its own per-action authorization: manual actions use
		// their optional role allowlist (or item.edit by default), while testing
		// event-driven actions remains action.manage-only.
		api.HandleH("POST /workspaces/{workspaceId}/actions/{id}/execute", auth(http.HandlerFunc(deps.Workspaces.Actions.ExecuteAction)))
		api.HandleH("GET /workspaces/{workspaceId}/actions/{id}/logs", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.GetActionLogs))))
		api.HandleH("GET /workspaces/{workspaceId}/action-logs", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.GetWorkspaceLogs))))

		// Capability listing shares action-authoring authorization.
		api.HandleH("GET /workspaces/{workspaceId}/action-capabilities", auth(actionManage(http.HandlerFunc(deps.Workspaces.Actions.ListWorkspaceCapabilities))))

		// Template reads are authenticated; applying requires action.manage.
		if deps.Workspaces.ActionTemplates != nil {
			api.HandleH("GET /action-templates", auth(http.HandlerFunc(deps.Workspaces.ActionTemplates.ListTemplates)))
			api.HandleH("POST /workspaces/{workspaceId}/action-templates/{templateKey}/apply", auth(actionManage(http.HandlerFunc(deps.Workspaces.ActionTemplates.CreateActionFromTemplate))))
		}

		api.HandleH("GET /admin/action-capabilities", admin(http.HandlerFunc(deps.Workspaces.Actions.ListCapabilities)))
		api.HandleH("POST /admin/action-capabilities", admin(http.HandlerFunc(deps.Workspaces.Actions.CreateCapability)))
		api.HandleH("GET /admin/action-capabilities/{capabilityId}", admin(http.HandlerFunc(deps.Workspaces.Actions.GetCapability)))
		api.HandleH("PUT /admin/action-capabilities/{capabilityId}", admin(http.HandlerFunc(deps.Workspaces.Actions.UpdateCapability)))
		api.HandleH("DELETE /admin/action-capabilities/{capabilityId}", admin(http.HandlerFunc(deps.Workspaces.Actions.DeleteCapability)))

		// Runner-pool child resources.
		if deps.Workspaces.RunnerControl != nil {
			api.HandleH("POST /admin/action-capabilities/{capabilityId}/runner-tokens", admin(http.HandlerFunc(deps.Workspaces.RunnerControl.MintRunnerToken)))
			api.HandleH("GET /admin/action-capabilities/{capabilityId}/runner-tokens", admin(http.HandlerFunc(deps.Workspaces.RunnerControl.ListRunnerTokens)))
			api.HandleH("DELETE /admin/action-capabilities/{capabilityId}/runner-tokens/{tokenId}", admin(http.HandlerFunc(deps.Workspaces.RunnerControl.RevokeRunnerToken)))
			api.HandleH("GET /admin/action-capabilities/{capabilityId}/runner-instances", admin(http.HandlerFunc(deps.Workspaces.RunnerControl.ListRunnerInstances)))
			api.HandleH("DELETE /admin/action-capabilities/{capabilityId}/runner-instances/{instanceId}", admin(http.HandlerFunc(deps.Workspaces.RunnerControl.RevokeRunnerInstance)))
		}

		if deps.Workspaces.ActionCredentials != nil {
			api.HandleH("GET /admin/action-credentials", admin(http.HandlerFunc(deps.Workspaces.ActionCredentials.ListGlobal)))
			api.HandleH("POST /admin/action-credentials", admin(http.HandlerFunc(deps.Workspaces.ActionCredentials.CreateGlobal)))
			api.HandleH("PUT /admin/action-credentials/{credentialId}", admin(http.HandlerFunc(deps.Workspaces.ActionCredentials.UpdateGlobal)))
			api.HandleH("POST /admin/action-credentials/{credentialId}/rotate", admin(http.HandlerFunc(deps.Workspaces.ActionCredentials.RotateGlobal)))
			api.HandleH("DELETE /admin/action-credentials/{credentialId}", admin(http.HandlerFunc(deps.Workspaces.ActionCredentials.DeleteGlobal)))

			// Handler authorization preserves 404-on-missing-workspace access.
			api.HandleH("GET /workspaces/{workspaceId}/action-credentials", auth(http.HandlerFunc(deps.Workspaces.ActionCredentials.ListForWorkspace)))
			api.HandleH("POST /workspaces/{workspaceId}/action-credentials", auth(http.HandlerFunc(deps.Workspaces.ActionCredentials.CreateForWorkspace)))
			api.HandleH("PUT /workspaces/{workspaceId}/action-credentials/{credentialId}", auth(http.HandlerFunc(deps.Workspaces.ActionCredentials.UpdateForWorkspace)))
			api.HandleH("POST /workspaces/{workspaceId}/action-credentials/{credentialId}/rotate", auth(http.HandlerFunc(deps.Workspaces.ActionCredentials.RotateForWorkspace)))
			api.HandleH("DELETE /workspaces/{workspaceId}/action-credentials/{credentialId}", auth(http.HandlerFunc(deps.Workspaces.ActionCredentials.DeleteForWorkspace)))
		}
	}
}
