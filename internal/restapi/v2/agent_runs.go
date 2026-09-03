package v2

import (
	"errors"
	"net/http"

	"windshift/internal/models"
	"windshift/internal/services"
)

func registerAgentRunRoutes(builder *routeBuilder, runs agentRunApplication) {
	builder.Read("/workspaces/{workspace_id}/agent-runs", AuthAuthenticated, []string{"items:read"}, listWorkspaceAgentRuns(runs))
	builder.Read("/items/{item_id}/agent-runs", AuthAuthenticated, []string{"items:read"}, listItemAgentRuns(runs))
	builder.Action(http.MethodPost, "/items/{item_id}/agent-runs", http.StatusOK, AuthAuthenticated, []string{"items:write"}, rerunAgent(runs))
	builder.Read("/agent-runs/{run_id}", AuthAuthenticated, []string{"items:read"}, getAgentRun(runs))
	builder.Read("/agent-runs/{run_id}/usage", AuthAuthenticated, []string{"items:read"}, getAgentRunUsage(runs))
	builder.Read("/agent-runs/{run_id}/events", AuthAuthenticated, []string{"items:read"}, listAgentRunEvents(runs))
	builder.Action(http.MethodPost, "/agent-runs/{run_id}/cancel", http.StatusOK, AuthAuthenticated, []string{"items:write"}, cancelAgentRun(runs))
}

func listWorkspaceAgentRuns(runs agentRunApplication) readOperation[[]*models.AgentRun] {
	return func(r *http.Request) ([]*models.AgentRun, error) {
		user, err := principal(r)
		if err != nil {
			return nil, err
		}
		workspaceID, err := pathID(r, "workspace_id")
		if err != nil {
			return nil, err
		}
		limit, beforeID, err := agentRunCursor(r)
		if err != nil {
			return nil, err
		}
		result, err := runs.ListForWorkspace(r.Context(), user.ID, workspaceID, limit, beforeID)
		return result, agentRunError(err)
	}
}

func listItemAgentRuns(runs agentRunApplication) readOperation[[]*models.AgentRun] {
	return func(r *http.Request) ([]*models.AgentRun, error) {
		user, err := principal(r)
		if err != nil {
			return nil, err
		}
		itemID, err := pathID(r, "item_id")
		if err != nil {
			return nil, err
		}
		limit, beforeID, err := agentRunCursor(r)
		if err != nil {
			return nil, err
		}
		result, err := runs.ListForItem(r.Context(), user.ID, itemID, limit, beforeID)
		return result, agentRunError(err)
	}
}

func rerunAgent(runs agentRunApplication) actionOperation[map[string]bool] {
	return func(r *http.Request) (map[string]bool, error) {
		user, err := principal(r)
		if err != nil {
			return nil, err
		}
		itemID, err := pathID(r, "item_id")
		if err != nil {
			return nil, err
		}
		started, err := runs.Rerun(r.Context(), user.ID, itemID)
		return map[string]bool{"started": started}, agentRunError(err)
	}
}

func getAgentRun(runs agentRunApplication) readOperation[*models.AgentRun] {
	return func(r *http.Request) (*models.AgentRun, error) {
		user, id, err := agentRunTarget(r)
		if err != nil {
			return nil, err
		}
		result, err := runs.Get(r.Context(), user.ID, id)
		return result, agentRunError(err)
	}
}

func getAgentRunUsage(runs agentRunApplication) readOperation[any] {
	return func(r *http.Request) (any, error) {
		user, id, err := agentRunTarget(r)
		if err != nil {
			return nil, err
		}
		result, err := runs.Usage(r.Context(), user.ID, id)
		return result, agentRunError(err)
	}
}

func listAgentRunEvents(runs agentRunApplication) readOperation[[]*models.AgentRunEvent] {
	return func(r *http.Request) ([]*models.AgentRunEvent, error) {
		user, id, err := agentRunTarget(r)
		if err != nil {
			return nil, err
		}
		afterID, err := parseNonNegativeQueryInt(r, "after_id", 0)
		if err != nil {
			return nil, err
		}
		limit, err := parsePositiveInt(r, "page_size", 200, 200)
		if err != nil {
			return nil, err
		}
		result, err := runs.Events(r.Context(), user.ID, id, afterID, limit)
		return result, agentRunError(err)
	}
}

func cancelAgentRun(runs agentRunApplication) actionOperation[services.AgentRunCancelResult] {
	return func(r *http.Request) (services.AgentRunCancelResult, error) {
		user, id, err := agentRunTarget(r)
		if err != nil {
			return services.AgentRunCancelResult{}, err
		}
		result, err := runs.Cancel(r.Context(), user.ID, id, r.URL.Query().Get("force") == "true")
		return result, agentRunError(err)
	}
}

func agentRunCursor(r *http.Request) (limit, beforeID int, err error) {
	limit, err = parsePositiveInt(r, "page_size", 50, 200)
	if err != nil {
		return 0, 0, err
	}
	beforeID, err = parseNonNegativeQueryInt(r, "before_id", 0)
	return limit, beforeID, err
}

func agentRunTarget(r *http.Request) (*models.User, int, error) {
	user, err := principal(r)
	if err != nil {
		return nil, 0, err
	}
	id, err := pathID(r, "run_id")
	return user, id, err
}

func agentRunError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, services.ErrAgentRunItemNotVisible), errors.Is(err, services.ErrAgentRunNotVisible):
		return newError(http.StatusNotFound, "not_found", "Agent run was not found")
	case errors.Is(err, services.ErrAgentRunUnavailable):
		return newError(http.StatusServiceUnavailable, "service_unavailable", err.Error())
	case errors.Is(err, services.ErrRerunNoPriorRun), errors.Is(err, services.ErrRerunNoBinding), errors.Is(err, services.ErrBindingBudgetExceeded):
		return newError(http.StatusConflict, "conflict", err.Error())
	default:
		return internalError(err)
	}
}
