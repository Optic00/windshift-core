// Package handlers — v1 test-management surface (WI-68 + WI-81).
//
// This file exposes the cookie test-management surface on /rest/api/v1:
// folders, cases, steps, labels, sets/plans, run templates, runs, reports,
// and result↔item links. The handlers use shared services/repositories so
// bearer-token and cookie-auth surfaces keep matching semantics without v1
// importing the legacy cookie-handler layer.
//
// Permission model: token-scope gating happens at the route layer
// (tests:read / tests:write). The handler additionally checks the caller's
// workspace test permission (test.view / test.manage / test.execute) via
// PermissionService — a token with tests:write still can't mutate catalog
// rows or drive runs in a workspace where its user lacks the matching role.
//
// Response shape: the legacy cookie handlers emit the resource directly
// (`respondJSONOK(w, payload)`), and we keep that for parity — v1 list
// endpoints conventionally use {"items":[...]}, but the existing CLI
// client and MCP tools expect bare arrays.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"windshift/internal/database"
	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/sanitize"
	"windshift/internal/services"
	"windshift/internal/testsummary"
)

// TestManagementHandler bundles the read + run-lifecycle endpoints into
// one handler so the route layer wires a single dependency. The
// services / repos it wraps are the same ones the cookie surface uses.
type TestManagementHandler struct {
	BaseHandler
	caseSvc        *services.TestCaseService
	folderSvc      *services.TestFolderService
	runSvc         *services.TestRunService
	setSvc         *services.TestSetService
	runTemplateSvc *services.TestRunTemplateService
	summaryRepo    *repository.TestSummaryRepository
	auditor        *logger.Auditor
}

// NewTestManagementHandler wires the v1 test-management handler. db /
// permissionService come from the v1 router; the rest is plumbing.
func NewTestManagementHandler(db database.Database, permissionService *services.PermissionService) *TestManagementHandler {
	caseSvc := services.NewTestCaseService(db)
	runSvc := services.NewTestRunService(db)
	auditor := logger.NewAuditor(db)

	return &TestManagementHandler{
		BaseHandler:    NewBaseHandler(db, permissionService),
		caseSvc:        caseSvc,
		folderSvc:      services.NewTestFolderService(db),
		runSvc:         runSvc,
		setSvc:         services.NewTestSetService(db),
		runTemplateSvc: services.NewTestRunTemplateService(db),
		summaryRepo:    repository.NewTestSummaryRepository(db),
		auditor:        auditor,
	}
}

// --- request / response shapes ---

type testRunCreateRequest struct {
	Name       string `json:"name"`
	TemplateID int    `json:"template_id"`
	SetID      int    `json:"set_id"`
	AssigneeID *int   `json:"assignee_id"`
}

type testResultUpdateRequest struct {
	Status       string `json:"status"`
	ActualResult string `json:"actual_result"`
	Notes        string `json:"notes"`
}

// --- workspace + test-permission helper ---

// requireTestWorkspace authenticates the caller, parses {workspaceId}
// from the path, and applies the workspace-level test permission check.
// 404 is returned on any failure (auth, parse, perm) so an unauthorized
// caller can't discriminate between "workspace doesn't exist" and
// "you don't have access" — matches the convention the rest of v1 uses
// for workspace-scoped resources.
//
// Callers that need the authenticated user can read it via
// middleware.GetUser after this returns; none of the lifecycle endpoints
// here need it, so it isn't returned to keep the signature tight.
func (h *TestManagementHandler) requireTestWorkspace(w http.ResponseWriter, r *http.Request, permission string) (workspaceID int, ok bool) {
	workspaceID, _, ok = h.requireTestWorkspaceUser(w, r, permission)
	return workspaceID, ok
}

func (h *TestManagementHandler) requireTestWorkspaceUser(w http.ResponseWriter, r *http.Request, permission string) (workspaceID int, user *models.User, ok bool) {
	user, ok = h.RequireAuth(w, r)
	if !ok {
		return 0, nil, false
	}
	workspaceID, ok = h.ParsePathID(w, r, "workspaceId", "workspace ID")
	if !ok {
		return 0, nil, false
	}
	allowed, err := h.PermissionService.HasWorkspacePermission(user.ID, workspaceID, permission)
	if err != nil || !allowed {
		h.RespondNotFound(w, r)
		return 0, nil, false
	}
	return workspaceID, user, true
}

func (h *TestManagementHandler) requireV1TestSetInWorkspace(w http.ResponseWriter, r *http.Request, permission string) (workspaceID, setID int, ok bool) {
	workspaceID, ok = h.requireTestWorkspace(w, r, permission)
	if !ok {
		return 0, 0, false
	}
	setID, ok = h.ParsePathID(w, r, "id", "test set ID")
	if !ok {
		return 0, 0, false
	}
	if _, err := h.setSvc.Get(setID, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return 0, 0, false
	}
	return workspaceID, setID, true
}

func (h *TestManagementHandler) requireV1RunTemplateInWorkspace(w http.ResponseWriter, r *http.Request, permission string) (workspaceID, templateID int, ok bool) {
	workspaceID, ok = h.requireTestWorkspace(w, r, permission)
	if !ok {
		return 0, 0, false
	}
	templateID, ok = h.ParsePathID(w, r, "id", "test run template ID")
	if !ok {
		return 0, 0, false
	}
	exists, err := h.runTemplateSvc.Exists(templateID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return 0, 0, false
	}
	if !exists {
		h.RespondNotFound(w, r)
		return 0, 0, false
	}
	return workspaceID, templateID, true
}

func (h *TestManagementHandler) respondV1Validation(w http.ResponseWriter, r *http.Request, message string) {
	h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, message))
}

// --- test cases ---

// ListTestCases handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases
//
// @Summary      List test cases in a workspace
// @Description  Optional `folder_id` query parameter filters to a single folder; pass `null` to retrieve top-level cases. `all=true` includes archived cases.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int     true   "Workspace ID"
// @Param        folder_id    query     string  false  "Folder ID or `null` for top-level cases"
// @Param        all          query     bool    false  "Include archived cases"
// @Success      200          {array}   models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or folder ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases [get]
func (h *TestManagementHandler) ListTestCases(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}

	params := services.TestCaseListParams{
		WorkspaceID: workspaceID,
		All:         r.URL.Query().Get("all") == "true",
		Search:      r.URL.Query().Get("q"),
	}
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 1 || limit > 250 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid limit"))
			return
		}
		params.Limit = limit
	}
	if rawOffset := r.URL.Query().Get("offset"); rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil || offset < 0 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid offset"))
			return
		}
		params.Offset = offset
	}
	if rawLabelID := r.URL.Query().Get("label_id"); rawLabelID != "" {
		labelID, err := strconv.Atoi(rawLabelID)
		if err != nil || labelID < 1 {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid label_id"))
			return
		}
		params.LabelID = &labelID
	}
	if folderIDParam := r.URL.Query().Get("folder_id"); folderIDParam != "" && folderIDParam != "null" {
		folderID, err := strconv.Atoi(folderIDParam)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid folder_id"))
			return
		}
		params.FolderID = &folderID
	}

	testCases, err := h.caseSvc.List(params)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, testCases)
}

// GetTestCaseCount handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/count.
//
// @Summary      Count test cases in a workspace
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {object}  map[string]int
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/count [get]
func (h *TestManagementHandler) GetTestCaseCount(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	count, err := h.caseSvc.CountAll(workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]int{"count": count})
}

// GetTestCase handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}
//
// @Summary      Get a test case by ID (scoped to workspace)
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test case ID"
// @Success      200          {object}  models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id} [get]
func (h *TestManagementHandler) GetTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test case ID")
	if !ok {
		return
	}
	tc, err := h.caseSvc.GetByID(id, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, tc)
}

// GetTestCaseSteps handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/steps
//
// @Summary      List steps on a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Success      200          {array}   models.TestStep
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or test case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/steps [get]
func (h *TestManagementHandler) GetTestCaseSteps(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	testCaseID, ok := h.ParsePathID(w, r, "testCaseId", "test case ID")
	if !ok {
		return
	}
	// Confirm workspace ownership before exposing the step.
	if _, err := h.caseSvc.GetByID(testCaseID, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	steps, err := h.caseSvc.GetSteps(testCaseID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, steps)
}

// --- test sets ---

// ListTestSets handles GET /rest/api/v1/workspaces/{workspaceId}/test-sets
//
// @Summary      List test sets in a workspace
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {array}   models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets [get]
func (h *TestManagementHandler) ListTestSets(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	sets, err := h.setSvc.List(workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, sets)
}

// GetTestSet handles GET /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}
//
// @Summary      Get a test set by ID
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test set ID"
// @Success      200          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or set ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id} [get]
func (h *TestManagementHandler) GetTestSet(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test set ID")
	if !ok {
		return
	}
	set, err := h.setSvc.Get(id, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, set)
}

// GetTestSetCases handles GET /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/test-cases
//
// @Summary      List test cases attached to a test set
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test set ID"
// @Success      200          {array}   models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or set ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id}/test-cases [get]
func (h *TestManagementHandler) GetTestSetCases(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	setID, ok := h.ParsePathID(w, r, "id", "test set ID")
	if !ok {
		return
	}
	testCases, err := h.setSvc.ListCases(setID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, testCases)
}

// --- test runs ---

// ListTestRuns handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs
//
// @Summary      List test runs in a workspace
// @Description  `assignee_id` filters to a single assignee; pass `unassigned` for runs with no assignee.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int     true   "Workspace ID"
// @Param        assignee_id  query     string  false  "Assignee user ID, or `unassigned`"
// @Success      200          {array}   models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs [get]
func (h *TestManagementHandler) ListTestRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	filters := services.TestRunListFilters{IncludeEnded: true}
	if a := r.URL.Query().Get("assignee_id"); a != "" {
		if a == "unassigned" {
			filters.Unassigned = true
		} else if id, err := strconv.Atoi(a); err == nil {
			filters.AssigneeID = &id
		} else {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid assignee_id"))
			return
		}
	}
	runs, err := h.runSvc.List(workspaceID, filters)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, runs)
}

// GetTestRun handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}
//
// @Summary      Get a test run by ID
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          {object}  models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id} [get]
func (h *TestManagementHandler) GetTestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	run, err := h.runSvc.GetByID(id, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, run)
}

// GetTestRunDetail handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/detail.
//
// @Summary      Get a complete test-run detail snapshot
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          {object}  services.TestRunDetail
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/detail [get]
func (h *TestManagementHandler) GetTestRunDetail(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	detail, err := h.runSvc.GetDetail(runID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, detail)
}

// CreateTestRun handles POST /rest/api/v1/workspaces/{workspaceId}/test-runs
//
// @Summary      Create a new test run (from a set or template)
// @Description  Pass `set_id` to start a run from a test set, or `template_id` to start from a saved run template. `name` is optional — the service generates one from the template when omitted.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                          true  "Workspace ID"
// @Param        body         body      handlers.testRunCreateRequest true  "Test run to create"
// @Success      201          {object}  models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.execute"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs [post]
func (h *TestManagementHandler) CreateTestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	var req testRunCreateRequest
	if !h.DecodeBodyOrRespond(w, r, &req) {
		return
	}
	run, err := h.runSvc.Create(workspaceID, services.TestRunCreateRequest{
		Name:       req.Name,
		TemplateID: req.TemplateID,
		SetID:      req.SetID,
		AssigneeID: req.AssigneeID,
	})
	if err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
		return
	}
	h.RespondCreated(w, run)
}

// EndTestRun handles POST /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/end
//
// @Summary      Mark a test run as ended
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          "Run marked complete"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/end [post]
func (h *TestManagementHandler) EndTestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	if err := h.runSvc.Complete(id, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

// GetTestRunResults handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/results
//
// @Summary      List per-test-case results in a test run
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          {array}   services.TestRunResultWithCaseTitle
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/results [get]
func (h *TestManagementHandler) GetTestRunResults(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	results, err := h.runSvc.ListResults(runID, workspaceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondOK(w, results)
}

// UpdateTestRunResult handles PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/results/{resultId}
//
// @Summary      Record / update a single test-case result in a run
// @Description  `status` is the canonical string the workspace uses ("passed", "failed", "blocked", "skipped"). `actual_result` and `notes` accept the same Markdown the SPA writes — server-side sanitization preserves blank-line `<br />` markers from MilkdownEditor.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                              true  "Workspace ID"
// @Param        id           path      int                              true  "Test run ID"
// @Param        resultId     path      int                              true  "Test result ID"
// @Param        body         body      handlers.testResultUpdateRequest true  "Result update"
// @Success      200          "Result updated"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run or result not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/results/{resultId} [put]
func (h *TestManagementHandler) UpdateTestRunResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	resultID, ok := h.ParsePathID(w, r, "resultId", "test result ID")
	if !ok {
		return
	}
	var req testResultUpdateRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return
	}
	if _, err := h.runSvc.UpdateResult(workspaceID, runID, resultID, services.TestResultUpdateRequest{
		Status:       req.Status,
		ActualResult: req.ActualResult,
		Notes:        req.Notes,
	}); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

// --- test run templates ---

// ExecuteTestRunTemplate handles POST /rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}/execute
//
// @Summary      Execute a saved test run template
// @Description  Creates a new test run from the template's bound set, with the run name auto-generated as `<template name> - Run <N>`.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run template ID"
// @Success      201          {object}  models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or template ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Template not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates/{id}/execute [post]
func (h *TestManagementHandler) ExecuteTestRunTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	templateID, ok := h.ParsePathID(w, r, "id", "test run template ID")
	if !ok {
		return
	}
	run, err := h.runTemplateSvc.Execute(templateID, workspaceID)
	if err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, run)
}

// --- test folders ---

// ListTestFolders handles GET /rest/api/v1/workspaces/{workspaceId}/test-folders
//
// @Summary      List test folders in a workspace
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {array}   models.TestFolder
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders [get]
func (h *TestManagementHandler) ListTestFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	folders, err := h.folderSvc.List(workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, folders)
}

// CreateTestFolder handles POST /rest/api/v1/workspaces/{workspaceId}/test-folders
//
// @Summary      Create a new test folder
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int               true  "Workspace ID"
// @Param        body         body      models.TestFolder true  "Test folder to create"
// @Success      201          {object}  models.TestFolder
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders [post]
func (h *TestManagementHandler) CreateTestFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var folder models.TestFolder
	if !h.DecodeBodyOrRespond(w, r, &folder) {
		return
	}
	created, err := h.folderSvc.Create(workspaceID, folder)
	if err != nil {
		h.respondTestFolderServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestFolderCreate, logger.ResourceTestFolder, &created.ID, created.Name)
	h.RespondCreated(w, created)
}

// GetTestFolder handles GET /rest/api/v1/workspaces/{workspaceId}/test-folders/{id}
//
// @Summary      Get a test folder by ID
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test folder ID"
// @Success      200          {object}  models.TestFolder
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or folder ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test folder not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders/{id} [get]
func (h *TestManagementHandler) GetTestFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test folder ID")
	if !ok {
		return
	}
	folder, err := h.folderSvc.Get(workspaceID, id)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, folder)
}

// UpdateTestFolder handles PUT /rest/api/v1/workspaces/{workspaceId}/test-folders/{id}
//
// @Summary      Update an existing test folder
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int               true  "Workspace ID"
// @Param        id           path      int               true  "Test folder ID"
// @Param        body         body      models.TestFolder true  "Test folder fields to update"
// @Success      200          {object}  models.TestFolder
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test folder not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders/{id} [put]
func (h *TestManagementHandler) UpdateTestFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test folder ID")
	if !ok {
		return
	}
	body, err := restapi.ReadJSONBody(w, r)
	if err != nil {
		if restapi.IsRequestBodyTooLarge(err) {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusRequestEntityTooLarge, restapi.ErrCodeRequestTooLarge, "Request body too large"))
			return
		}
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return
	}
	var folder models.TestFolder
	if err := json.Unmarshal(body, &folder); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return
	}
	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawPayload); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return
	}
	updated, err := h.folderSvc.Update(workspaceID, id, services.TestFolderUpdateInput{
		Folder:            folder,
		ParentProvided:    rawPayloadHas(rawPayload, "parent_id"),
		SortOrderProvided: rawPayloadHas(rawPayload, "sort_order"),
	})
	if err != nil {
		h.respondTestFolderServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestFolderUpdate, logger.ResourceTestFolder, &id, updated.Name)
	h.RespondOK(w, updated)
}

// DeleteTestFolder handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-folders/{id}
//
// @Summary      Delete a test folder
// @Description  Test cases inside the folder are moved to no folder (not deleted).
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test folder ID"
// @Success      204          "Folder deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or folder ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test folder not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders/{id} [delete]
func (h *TestManagementHandler) DeleteTestFolder(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test folder ID")
	if !ok {
		return
	}
	if err := h.folderSvc.Delete(workspaceID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestFolderDelete, logger.ResourceTestFolder, &id, "")
	h.RespondNoContent(w)
}

// ReorderTestFolders handles PUT /rest/api/v1/workspaces/{workspaceId}/test-folders/reorder
//
// @Summary      Reorder test folders in a workspace
// @Description  Body is `{"folder_ids":[...]}` — the desired top-down order.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        body         body      map[string][]int        true  "Folder IDs in desired order"
// @Success      200          "Folders reordered"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-folders/reorder [put]
func (h *TestManagementHandler) ReorderTestFolders(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var reorderData struct {
		FolderIDs []int `json:"folder_ids"`
	}
	if !h.DecodeBodyOrRespond(w, r, &reorderData) {
		return
	}
	if err := h.folderSvc.Reorder(workspaceID, reorderData.FolderIDs); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

func rawPayloadHas(rawPayload map[string]json.RawMessage, key string) bool {
	_, ok := rawPayload[key]
	return ok
}

func (h *TestManagementHandler) respondTestFolderServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		h.RespondNotFound(w, r)
	case errors.Is(err, services.ErrTestFolderNameRequired):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Folder name is required"))
	case errors.Is(err, services.ErrTestFolderParentNotFound),
		errors.Is(err, services.ErrTestFolderNestedDepth),
		errors.Is(err, services.ErrTestFolderParentSelf),
		errors.Is(err, services.ErrTestFolderParentHasChildren):
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
	default:
		h.RespondInternalError(w, r)
	}
}

func (h *TestManagementHandler) respondTestCaseServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
}

func (h *TestManagementHandler) requireV1TestCaseInWorkspace(w http.ResponseWriter, r *http.Request, permission string) (workspaceID, testCaseID int, ok bool) {
	workspaceID, ok = h.requireTestWorkspace(w, r, permission)
	if !ok {
		return 0, 0, false
	}
	testCaseID, ok = h.ParsePathID(w, r, "testCaseId", "test case ID")
	if !ok {
		return 0, 0, false
	}
	exists, err := h.caseSvc.Exists(testCaseID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return 0, 0, false
	}
	if !exists {
		h.RespondNotFound(w, r)
		return 0, 0, false
	}
	return workspaceID, testCaseID, true
}

func (h *TestManagementHandler) decodeV1TestSetWrite(w http.ResponseWriter, r *http.Request, permission string) (int, *models.User, models.TestSet, bool) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, permission)
	if !ok {
		return 0, nil, models.TestSet{}, false
	}
	var set models.TestSet
	if !h.DecodeBodyOrRespond(w, r, &set) {
		return 0, nil, models.TestSet{}, false
	}
	return workspaceID, user, set, true
}

func (h *TestManagementHandler) decodeV1RunTemplateWrite(w http.ResponseWriter, r *http.Request, permission string) (int, models.TestRunTemplate, bool) {
	workspaceID, ok := h.requireTestWorkspace(w, r, permission)
	if !ok {
		return 0, models.TestRunTemplate{}, false
	}
	var template models.TestRunTemplate
	if !h.DecodeBodyOrRespond(w, r, &template) {
		return 0, models.TestRunTemplate{}, false
	}
	return workspaceID, template, true
}

func (h *TestManagementHandler) respondTestSetServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrTestSetMilestoneNotFound):
		h.respondV1Validation(w, r, err.Error())
	case errors.Is(err, services.ErrTestSetCaseNotFound), errors.Is(err, repository.ErrNotFound):
		h.RespondNotFound(w, r)
	default:
		h.RespondInternalError(w, r)
	}
}

func (h *TestManagementHandler) respondRunTemplateServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, services.ErrTestRunTemplateSetNotFound) || errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	h.RespondInternalError(w, r)
}

type v1TestLabelInput struct {
	Name        string
	Color       string
	Description string
}

func (h *TestManagementHandler) decodeV1TestLabelInput(w http.ResponseWriter, r *http.Request) (v1TestLabelInput, bool) {
	var raw struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if !h.DecodeBodyOrRespond(w, r, &raw) {
		return v1TestLabelInput{}, false
	}
	raw.Name = sanitize.ShortIdentifier.Sanitize(raw.Name)
	raw.Description = sanitize.RichText.Sanitize(raw.Description)
	if raw.Name == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Label name is required"))
		return v1TestLabelInput{}, false
	}
	return v1TestLabelInput{Name: raw.Name, Color: raw.Color, Description: raw.Description}, true
}

// CreateTestCase handles POST /rest/api/v1/workspaces/{workspaceId}/test-cases
//
// @Summary      Create a new test case
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int             true  "Workspace ID"
// @Param        body         body      models.TestCase true  "Test case to create"
// @Success      201          {object}  models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases [post]
func (h *TestManagementHandler) CreateTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var input struct {
		Title             string `json:"title"`
		Preconditions     string `json:"preconditions"`
		Priority          string `json:"priority"`
		Status            string `json:"status"`
		EstimatedDuration int    `json:"estimated_duration"`
		FolderID          *int   `json:"folder_id"`
	}
	if !h.DecodeBodyOrRespond(w, r, &input) {
		return
	}
	if sanitize.PlainTextField.Sanitize(input.Title) == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Test case title is required"))
		return
	}
	testCase, err := h.caseSvc.Create(workspaceID, services.TestCaseCreateRequest{
		Title:             input.Title,
		Preconditions:     input.Preconditions,
		Priority:          input.Priority,
		Status:            input.Status,
		EstimatedDuration: input.EstimatedDuration,
		FolderID:          input.FolderID,
	})
	if err != nil {
		h.respondTestCaseServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestCaseCreate, logger.ResourceTestCase, &testCase.ID, testCase.Title)
	h.RespondCreated(w, testCase)
}

// UpdateTestCase handles PUT /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}
//
// @Summary      Update an existing test case
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int             true  "Workspace ID"
// @Param        id           path      int             true  "Test case ID"
// @Param        body         body      models.TestCase true  "Test case fields to update"
// @Success      200          {object}  models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id} [put]
func (h *TestManagementHandler) UpdateTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test case ID")
	if !ok {
		return
	}
	var input struct {
		Title             string `json:"title"`
		Preconditions     string `json:"preconditions"`
		Priority          string `json:"priority"`
		Status            string `json:"status"`
		EstimatedDuration int    `json:"estimated_duration"`
		FolderID          *int   `json:"folder_id"`
		SortOrder         int    `json:"sort_order"`
	}
	if !h.DecodeBodyOrRespond(w, r, &input) {
		return
	}
	if sanitize.PlainTextField.Sanitize(input.Title) == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Test case title is required"))
		return
	}
	testCase, err := h.caseSvc.Update(id, workspaceID, services.TestCaseUpdateRequest{
		Title:             input.Title,
		Preconditions:     input.Preconditions,
		Priority:          input.Priority,
		Status:            input.Status,
		EstimatedDuration: input.EstimatedDuration,
		FolderID:          input.FolderID,
		SortOrder:         input.SortOrder,
	})
	if err != nil {
		h.respondTestCaseServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestCaseUpdate, logger.ResourceTestCase, &testCase.ID, testCase.Title)
	h.RespondOK(w, testCase)
}

// DeleteTestCase handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}
//
// @Summary      Delete a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test case ID"
// @Success      204          "Test case deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id} [delete]
func (h *TestManagementHandler) DeleteTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test case ID")
	if !ok {
		return
	}
	if err := h.caseSvc.Delete(id, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestCaseDelete, logger.ResourceTestCase, &id, "")
	h.RespondNoContent(w)
}

// MoveTestCase handles PUT /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}/move
//
// @Summary      Move a test case to a different folder
// @Description  Body is `{"folder_id":<id|null>, "sort_order":<int>}`. Passing `null` for `folder_id` moves the case to the top level.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        id           path      int                     true  "Test case ID"
// @Param        body         body      map[string]any  true  "Folder ID + sort order"
// @Success      200          "Test case moved"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id}/move [put]
func (h *TestManagementHandler) MoveTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test case ID")
	if !ok {
		return
	}
	var moveData struct {
		FolderID  *int `json:"folder_id"`
		SortOrder int  `json:"sort_order"`
	}
	if !h.DecodeBodyOrRespond(w, r, &moveData) {
		return
	}
	if err := h.caseSvc.Move(id, workspaceID, moveData.FolderID, moveData.SortOrder); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

// ReorderTestCases handles PUT /rest/api/v1/workspaces/{workspaceId}/test-cases/reorder
//
// @Summary      Reorder test cases within a folder
// @Description  Body is `{"folder_id":<id|null>, "test_case_ids":[...]}` — the desired order.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        body         body      map[string]any  true  "Folder ID + test case IDs in desired order"
// @Success      200          "Test cases reordered"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/reorder [put]
func (h *TestManagementHandler) ReorderTestCases(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var reorderData struct {
		FolderID    *int  `json:"folder_id"`
		TestCaseIDs []int `json:"test_case_ids"`
	}
	if !h.DecodeBodyOrRespond(w, r, &reorderData) {
		return
	}
	if err := h.caseSvc.Reorder(workspaceID, reorderData.TestCaseIDs); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

// GetTestCaseConnections handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}/connections
//
// @Summary      List related sets, templates, and executions for a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test case ID"
// @Success      200          {object}  map[string]any
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id}/connections [get]
func (h *TestManagementHandler) GetTestCaseConnections(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test case ID")
	if !ok {
		return
	}
	exists, err := h.caseSvc.Exists(id, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !exists {
		h.RespondNotFound(w, r)
		return
	}
	connections, err := h.caseSvc.GetConnections(id, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, connections)
}

// CreateTestCaseStep handles POST /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/steps
//
// @Summary      Append a step to a test case
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int             true  "Workspace ID"
// @Param        testCaseId   path      int             true  "Test case ID"
// @Param        body         body      models.TestStep true  "Test step to create"
// @Success      201          {object}  models.TestStep
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/steps [post]
func (h *TestManagementHandler) CreateTestCaseStep(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var input struct {
		Action   string `json:"action"`
		Data     string `json:"data"`
		Expected string `json:"expected"`
	}
	if !h.DecodeBodyOrRespond(w, r, &input) {
		return
	}
	if input.Action == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Test step action is required"))
		return
	}
	step, err := h.caseSvc.CreateStep(testCaseID, services.TestStepCreateRequest{Action: input.Action, Data: input.Data, Expected: input.Expected})
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, step)
}

// UpdateTestCaseStep handles PUT /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId}
//
// @Summary      Update an existing test step
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int             true  "Workspace ID"
// @Param        testCaseId   path      int             true  "Test case ID"
// @Param        stepId       path      int             true  "Test step ID"
// @Param        body         body      models.TestStep true  "Test step fields to update"
// @Success      200          {object}  models.TestStep
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test step not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId} [put]
func (h *TestManagementHandler) UpdateTestCaseStep(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	stepID, ok := h.ParsePathID(w, r, "stepId", "test step ID")
	if !ok {
		return
	}
	var input struct {
		StepNumber int    `json:"step_number"`
		Action     string `json:"action"`
		Data       string `json:"data"`
		Expected   string `json:"expected"`
	}
	if !h.DecodeBodyOrRespond(w, r, &input) {
		return
	}
	if input.Action == "" {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, "Test step action is required"))
		return
	}
	step, err := h.caseSvc.UpdateStep(stepID, testCaseID, services.TestStepUpdateRequest{StepNumber: input.StepNumber, Action: input.Action, Data: input.Data, Expected: input.Expected})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, step)
}

// DeleteTestCaseStep handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId}
//
// @Summary      Delete a test step
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Param        stepId       path      int  true  "Test step ID"
// @Success      204          "Test step deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace, case, or step ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test step not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/{stepId} [delete]
func (h *TestManagementHandler) DeleteTestCaseStep(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	stepID, ok := h.ParsePathID(w, r, "stepId", "test step ID")
	if !ok {
		return
	}
	if err := h.caseSvc.DeleteStep(stepID, testCaseID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// ReorderTestCaseSteps handles PUT /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/steps/reorder
//
// @Summary      Reorder steps on a test case
// @Description  Body is `{"step_ids":[...]}` — the desired step order.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        testCaseId   path      int                     true  "Test case ID"
// @Param        body         body      map[string][]int        true  "Step IDs in desired order"
// @Success      200          "Test steps reordered"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/steps/reorder [put]
func (h *TestManagementHandler) ReorderTestCaseSteps(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var reorderData struct {
		StepIDs []int `json:"step_ids"`
	}
	if !h.DecodeBodyOrRespond(w, r, &reorderData) {
		return
	}
	if err := h.caseSvc.ReorderSteps(testCaseID, reorderData.StepIDs); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]bool{"success": true})
}

// ListTestLabels handles GET /rest/api/v1/workspaces/{workspaceId}/test-labels
//
// @Summary      List all test labels in a workspace
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {array}   models.TestLabel
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-labels [get]
func (h *TestManagementHandler) ListTestLabels(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	labels, err := h.caseSvc.GetAllLabels(workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, labels)
}

// CreateTestLabel handles POST /rest/api/v1/workspaces/{workspaceId}/test-labels
//
// @Summary      Create a new test label in a workspace
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int               true  "Workspace ID"
// @Param        body         body      models.TestLabel  true  "Test label to create"
// @Success      201          {object}  models.TestLabel
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-labels [post]
func (h *TestManagementHandler) CreateTestLabel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	input, ok := h.decodeV1TestLabelInput(w, r)
	if !ok {
		return
	}
	label, err := h.caseSvc.CreateLabel(workspaceID, services.TestLabelCreateRequest{Name: input.Name, Color: input.Color, Description: input.Description})
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, label)
}

// UpdateTestLabel handles PUT /rest/api/v1/workspaces/{workspaceId}/test-labels/{labelId}
//
// @Summary      Update an existing test label
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int               true  "Workspace ID"
// @Param        labelId      path      int               true  "Test label ID"
// @Param        body         body      models.TestLabel  true  "Test label fields to update"
// @Success      200          {object}  models.TestLabel
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test label not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-labels/{labelId} [put]
func (h *TestManagementHandler) UpdateTestLabel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	labelID, ok := h.ParsePathID(w, r, "labelId", "test label ID")
	if !ok {
		return
	}
	input, ok := h.decodeV1TestLabelInput(w, r)
	if !ok {
		return
	}
	label, err := h.caseSvc.UpdateLabel(labelID, workspaceID, services.TestLabelUpdateRequest{Name: input.Name, Color: input.Color, Description: input.Description})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, label)
}

// DeleteTestLabel handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-labels/{labelId}
//
// @Summary      Delete a test label
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        labelId      path      int  true  "Test label ID"
// @Success      204          "Test label deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or label ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test label not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-labels/{labelId} [delete]
func (h *TestManagementHandler) DeleteTestLabel(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	labelID, ok := h.ParsePathID(w, r, "labelId", "test label ID")
	if !ok {
		return
	}
	if err := h.caseSvc.DeleteLabel(labelID, workspaceID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// ListTestCaseLabels handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/labels
//
// @Summary      List labels attached to a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Success      200          {array}   models.TestLabel
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/labels [get]
func (h *TestManagementHandler) ListTestCaseLabels(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	labels, err := h.caseSvc.GetLabelsForTestCase(testCaseID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, labels)
}

// AddTestCaseLabel handles POST /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/labels
//
// @Summary      Attach a label to a test case
// @Description  Body is `{"label_id":<id>}`.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                  true  "Workspace ID"
// @Param        testCaseId   path      int                  true  "Test case ID"
// @Param        body         body      map[string]int       true  "Label ID to attach"
// @Success      201          "Label attached"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case or label not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/labels [post]
func (h *TestManagementHandler) AddTestCaseLabel(w http.ResponseWriter, r *http.Request) {
	workspaceID, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var data struct {
		LabelID int `json:"label_id"`
	}
	if !h.DecodeBodyOrRespond(w, r, &data) {
		return
	}
	if err := h.caseSvc.AddLabelToTestCase(testCaseID, data.LabelID, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, map[string]bool{"success": true})
}

// RemoveTestCaseLabel handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-cases/{testCaseId}/labels/{labelId}
//
// @Summary      Detach a label from a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Param        labelId      path      int  true  "Test label ID"
// @Success      204          "Label detached"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace, case, or label ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{testCaseId}/labels/{labelId} [delete]
func (h *TestManagementHandler) RemoveTestCaseLabel(w http.ResponseWriter, r *http.Request) {
	_, testCaseID, ok := h.requireV1TestCaseInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	labelID, ok := h.ParsePathID(w, r, "labelId", "test label ID")
	if !ok {
		return
	}
	if err := h.caseSvc.RemoveLabelFromTestCase(testCaseID, labelID); err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondNoContent(w)
}

// CreateTestSet handles POST /rest/api/v1/workspaces/{workspaceId}/test-sets
//
// @Summary      Create a new test set
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int            true  "Workspace ID"
// @Param        body         body      models.TestSet true  "Test set to create"
// @Success      201          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets [post]
func (h *TestManagementHandler) CreateTestSet(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, set, ok := h.decodeV1TestSetWrite(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	created, err := h.setSvc.Create(workspaceID, set)
	if err != nil {
		h.respondTestSetServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestSetCreate, logger.ResourceTestSet, &created.ID, created.Name)
	h.RespondCreated(w, created)
}

// UpdateTestSet handles PUT /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}
//
// @Summary      Update an existing test set
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int            true  "Workspace ID"
// @Param        id           path      int            true  "Test set ID"
// @Param        body         body      models.TestSet true  "Test set fields to update"
// @Success      200          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id} [put]
func (h *TestManagementHandler) UpdateTestSet(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, set, ok := h.decodeV1TestSetWrite(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test set ID")
	if !ok {
		return
	}
	updated, err := h.setSvc.Update(id, workspaceID, set)
	if err != nil {
		h.respondTestSetServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestSetUpdate, logger.ResourceTestSet, &id, updated.Name)
	h.RespondOK(w, updated)
}

// DeleteTestSet handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}
//
// @Summary      Delete a test set
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test set ID"
// @Success      204          "Test set deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or set ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id} [delete]
func (h *TestManagementHandler) DeleteTestSet(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test set ID")
	if !ok {
		return
	}
	if err := h.setSvc.Delete(id, workspaceID); err != nil {
		h.respondTestSetServiceError(w, r, err)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestSetDelete, logger.ResourceTestSet, &id, "")
	h.RespondNoContent(w)
}

// AddTestSetCase handles POST /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/test-cases
//
// @Summary      Attach a test case to a test set
// @Description  Body is `{"test_case_id":<id>}`.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                  true  "Workspace ID"
// @Param        id           path      int                  true  "Test set ID"
// @Param        body         body      map[string]int       true  "Test case ID to attach"
// @Success      201          "Test case attached to set"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set or case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id}/test-cases [post]
func (h *TestManagementHandler) AddTestSetCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := h.requireV1TestSetInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	var request struct {
		TestCaseID int `json:"test_case_id"`
	}
	if !h.DecodeBodyOrRespond(w, r, &request) {
		return
	}
	if err := h.setSvc.AddCase(setID, request.TestCaseID, workspaceID); err != nil {
		h.respondTestSetServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, nil)
}

// RemoveTestSetCase handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/test-cases/{testCaseId}
//
// @Summary      Detach a test case from a test set
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test set ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Success      204          "Test case detached from set"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace, set, or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id}/test-cases/{testCaseId} [delete]
func (h *TestManagementHandler) RemoveTestSetCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := h.requireV1TestSetInWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	testCaseID, ok := h.ParsePathID(w, r, "testCaseId", "test case ID")
	if !ok {
		return
	}
	if err := h.setSvc.RemoveCase(setID, testCaseID, workspaceID); err != nil {
		h.respondTestSetServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// ListTestSetRuns handles GET /rest/api/v1/workspaces/{workspaceId}/test-sets/{id}/runs
//
// @Summary      List test runs created from a test set
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test set ID"
// @Success      200          {array}   models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or set ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test set not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-sets/{id}/runs [get]
func (h *TestManagementHandler) ListTestSetRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := h.requireV1TestSetInWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runs, err := h.setSvc.ListRuns(setID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, runs)
}

// ListTestPlans handles GET /rest/api/v1/workspaces/{workspaceId}/test-plans
//
// @Summary      List test plans in a workspace
// @Description  Test plans share the underlying `test_sets` table — this is an alias surface for clients that prefer "plan" terminology.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {array}   models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans [get]
func (h *TestManagementHandler) ListTestPlans(w http.ResponseWriter, r *http.Request) {
	h.ListTestSets(w, r)
}

// CreateTestPlan handles POST /rest/api/v1/workspaces/{workspaceId}/test-plans
//
// @Summary      Create a new test plan
// @Description  Alias for `POST /workspaces/{workspaceId}/test-sets` — same persistence, different terminology.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int            true  "Workspace ID"
// @Param        body         body      models.TestSet true  "Test plan to create"
// @Success      201          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans [post]
func (h *TestManagementHandler) CreateTestPlan(w http.ResponseWriter, r *http.Request) {
	h.CreateTestSet(w, r)
}

// GetTestPlan handles GET /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}
//
// @Summary      Get a test plan by ID
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test plan ID"
// @Success      200          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or plan ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id} [get]
func (h *TestManagementHandler) GetTestPlan(w http.ResponseWriter, r *http.Request) {
	h.GetTestSet(w, r)
}

// UpdateTestPlan handles PUT /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}
//
// @Summary      Update an existing test plan
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int            true  "Workspace ID"
// @Param        id           path      int            true  "Test plan ID"
// @Param        body         body      models.TestSet true  "Test plan fields to update"
// @Success      200          {object}  models.TestSet
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id} [put]
func (h *TestManagementHandler) UpdateTestPlan(w http.ResponseWriter, r *http.Request) {
	h.UpdateTestSet(w, r)
}

// DeleteTestPlan handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}
//
// @Summary      Delete a test plan
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test plan ID"
// @Success      204          "Test plan deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or plan ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id} [delete]
func (h *TestManagementHandler) DeleteTestPlan(w http.ResponseWriter, r *http.Request) {
	h.DeleteTestSet(w, r)
}

// ListTestPlanCases handles GET /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}/test-cases
//
// @Summary      List test cases attached to a test plan
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test plan ID"
// @Success      200          {array}   models.TestCase
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or plan ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id}/test-cases [get]
func (h *TestManagementHandler) ListTestPlanCases(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := h.requireV1TestSetInWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	testCases, err := h.setSvc.ListCases(setID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, testCases)
}

// AddTestPlanCase handles POST /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}/test-cases
//
// @Summary      Attach a test case to a test plan
// @Description  Body is `{"test_case_id":<id>}`.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                  true  "Workspace ID"
// @Param        id           path      int                  true  "Test plan ID"
// @Param        body         body      map[string]int       true  "Test case ID to attach"
// @Success      201          "Test case attached to plan"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan or case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id}/test-cases [post]
func (h *TestManagementHandler) AddTestPlanCase(w http.ResponseWriter, r *http.Request) {
	h.AddTestSetCase(w, r)
}

// RemoveTestPlanCase handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}/test-cases/{testCaseId}
//
// @Summary      Detach a test case from a test plan
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test plan ID"
// @Param        testCaseId   path      int  true  "Test case ID"
// @Success      204          "Test case detached from plan"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace, plan, or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id}/test-cases/{testCaseId} [delete]
func (h *TestManagementHandler) RemoveTestPlanCase(w http.ResponseWriter, r *http.Request) {
	h.RemoveTestSetCase(w, r)
}

// ListTestPlanRuns handles GET /rest/api/v1/workspaces/{workspaceId}/test-plans/{id}/runs
//
// @Summary      List test runs created from a test plan
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test plan ID"
// @Success      200          {array}   models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or plan ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test plan not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-plans/{id}/runs [get]
func (h *TestManagementHandler) ListTestPlanRuns(w http.ResponseWriter, r *http.Request) {
	h.ListTestSetRuns(w, r)
}

// ListTestRunTemplates handles GET /rest/api/v1/workspaces/{workspaceId}/test-run-templates
//
// @Summary      List test run templates in a workspace
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Success      200          {array}   models.TestRunTemplate
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates [get]
func (h *TestManagementHandler) ListTestRunTemplates(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	templates, err := h.runTemplateSvc.List(workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, templates)
}

// CreateTestRunTemplate handles POST /rest/api/v1/workspaces/{workspaceId}/test-run-templates
//
// @Summary      Create a new test run template
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                    true  "Workspace ID"
// @Param        body         body      models.TestRunTemplate true  "Test run template to create"
// @Success      201          {object}  models.TestRunTemplate
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates [post]
func (h *TestManagementHandler) CreateTestRunTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, template, ok := h.decodeV1RunTemplateWrite(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	created, err := h.runTemplateSvc.Create(workspaceID, template)
	if err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondCreated(w, created)
}

// GetTestRunTemplate handles GET /rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}
//
// @Summary      Get a test run template by ID
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run template ID"
// @Success      200          {object}  models.TestRunTemplate
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or template ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run template not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates/{id} [get]
func (h *TestManagementHandler) GetTestRunTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run template ID")
	if !ok {
		return
	}
	template, err := h.runTemplateSvc.Get(id, workspaceID)
	if err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondOK(w, template)
}

// UpdateTestRunTemplate handles PUT /rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}
//
// @Summary      Update an existing test run template
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                    true  "Workspace ID"
// @Param        id           path      int                    true  "Test run template ID"
// @Param        body         body      models.TestRunTemplate true  "Test run template fields to update"
// @Success      200          {object}  models.TestRunTemplate
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run template not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates/{id} [put]
func (h *TestManagementHandler) UpdateTestRunTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, template, ok := h.decodeV1RunTemplateWrite(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run template ID")
	if !ok {
		return
	}
	updated, err := h.runTemplateSvc.Update(id, workspaceID, template)
	if err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondOK(w, updated)
}

// DeleteTestRunTemplate handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}
//
// @Summary      Delete a test run template
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run template ID"
// @Success      204          "Template deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or template ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run template not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates/{id} [delete]
func (h *TestManagementHandler) DeleteTestRunTemplate(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run template ID")
	if !ok {
		return
	}
	if err := h.runTemplateSvc.Delete(id, workspaceID); err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondNoContent(w)
}

// ListTestRunTemplateExecutions handles GET /rest/api/v1/workspaces/{workspaceId}/test-run-templates/{id}/executions
//
// @Summary      List test runs created from a template
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run template ID"
// @Success      200          {array}   models.TestRun
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or template ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run template not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-run-templates/{id}/executions [get]
func (h *TestManagementHandler) ListTestRunTemplateExecutions(w http.ResponseWriter, r *http.Request) {
	workspaceID, templateID, ok := h.requireV1RunTemplateInWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runs, err := h.runTemplateSvc.ListExecutions(templateID, workspaceID)
	if err != nil {
		h.respondRunTemplateServiceError(w, r, err)
		return
	}
	h.RespondOK(w, runs)
}

// UpdateTestRun handles PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}
//
// @Summary      Update an existing test run (name / assignee)
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        id           path      int                     true  "Test run ID"
// @Param        body         body      map[string]any  true  "Test run fields to update (name, assignee_id)"
// @Success      200          "Test run updated"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id} [put]
func (h *TestManagementHandler) UpdateTestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	var input struct {
		Name       string `json:"name"`
		AssigneeID *int   `json:"assignee_id"`
	}
	if !h.DecodeBodyOrRespond(w, r, &input) {
		return
	}
	if _, err := h.runSvc.Update(id, workspaceID, services.TestRunUpdateRequest{Name: input.Name, AssigneeID: input.AssigneeID}); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.respondV1Validation(w, r, err.Error())
		return
	}
	h.auditor.Log(r, user, logger.ActionTestRunUpdate, logger.ResourceTestRun, &id, "")
	h.RespondOK(w, map[string]bool{"success": true})
}

// DeleteTestRun handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}
//
// @Summary      Delete a test run and its associated results
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          "Test run deleted"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id} [delete]
func (h *TestManagementHandler) DeleteTestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, ok := h.requireTestWorkspaceUser(w, r, models.PermissionTestManage)
	if !ok {
		return
	}
	id, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	if err := h.runSvc.Delete(id, workspaceID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
			return
		}
		h.RespondInternalError(w, r)
		return
	}
	h.auditor.Log(r, user, logger.ActionTestRunDelete, logger.ResourceTestRun, &id, "")
	w.WriteHeader(http.StatusOK)
}

// GetTestRunStepResults handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/steps
//
// @Summary      List per-step results in a test run
// @Description  Returns a map keyed by `<test_case_id>_<step_id>` so clients can look up step results by composite key in one pass.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          {object}  map[string]any
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/steps [get]
func (h *TestManagementHandler) GetTestRunStepResults(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	stepResults, err := h.runSvc.ListStepResults(runID, workspaceID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondOK(w, stepResults)
}

// UpdateTestRunStepResult handles PUT /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/steps/{stepId}
//
// @Summary      Record / update a single step result in a run
// @Description  `status` is one of "passed", "failed", "blocked", "skipped", "not_run". `actual_result` and `notes` accept the same Markdown the SPA writes; server-side sanitization preserves blank-line `<br />` markers from MilkdownEditor. Optional `item_id` links a work item to the step result.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                     true  "Workspace ID"
// @Param        id           path      int                     true  "Test run ID"
// @Param        stepId       path      int                     true  "Test step ID"
// @Param        body         body      map[string]any  true  "Step result update"
// @Success      200          "Step result recorded"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run, step, or item not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/steps/{stepId} [put]
func (h *TestManagementHandler) UpdateTestRunStepResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	stepID, ok := h.ParsePathID(w, r, "stepId", "test step ID")
	if !ok {
		return
	}
	var update struct {
		Status       string `json:"status"`
		ActualResult string `json:"actual_result"`
		Notes        string `json:"notes"`
		ItemID       *int   `json:"item_id,omitempty"`
	}
	if !h.DecodeBodyOrRespond(w, r, &update) {
		return
	}
	if err := h.runSvc.UpdateStepResult(workspaceID, runID, stepID, services.TestStepResultUpdateRequest{
		Status: update.Status, ActualResult: update.ActualResult, Notes: update.Notes, ItemID: update.ItemID,
	}); err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound), errors.Is(err, services.ErrTestRunItemNotFound):
			h.RespondNotFound(w, r)
		case errors.Is(err, services.ErrInvalidTestResultStatus):
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeValidationFailed, err.Error()))
		default:
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondOK(w, map[string]string{"status": "success"})
}

// GetTestRunSummary handles GET /rest/api/v1/workspaces/{workspaceId}/test-runs/{id}/summary
//
// @Summary      Get a Markdown summary of a test run
// @Description  Returns `{"markdown": "<rendered summary>"}` — header, statistics table, failed/blocked sections, and full result table. Sanitization is the client renderer's responsibility.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test run ID"
// @Success      200          {object}  map[string]string
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/summary [get]
func (h *TestManagementHandler) GetTestRunSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	runID, ok := h.ParsePathID(w, r, "id", "test run ID")
	if !ok {
		return
	}
	header, err := h.summaryRepo.FindMarkdownRunHeader(runID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondNotFound(w, r)
		return
	}
	results, err := h.summaryRepo.FindMarkdownResults(runID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]string{"markdown": testsummary.RenderMarkdown(header, results)})
}

// GetTestReportsSummary handles GET /rest/api/v1/workspaces/{workspaceId}/test-reports/summary
//
// @Summary      Get aggregate test reports for a workspace
// @Description  Returns overall stats, trend, recent failures, and recent blocked tests. Optional `milestone_id` and `days` (1-365, default 30) query parameters scope the report.
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId   path      int     true   "Workspace ID"
// @Param        milestone_id  query     int     false  "Milestone ID to scope the report to"
// @Param        days          query     int     false  "Window (1-365 days, default 30)"
// @Success      200           {object}  map[string]any
// @Failure      400           {object}  handlers.ErrorResponse  "Invalid query parameters"
// @Failure      401           {object}  handlers.ErrorResponse
// @Failure      403           {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404           {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500           {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-reports/summary [get]
func (h *TestManagementHandler) GetTestReportsSummary(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	var milestoneID *int
	if milestoneIDStr := r.URL.Query().Get("milestone_id"); milestoneIDStr != "" {
		mid, err := strconv.Atoi(milestoneIDStr)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid milestone_id"))
			return
		}
		milestoneID = &mid
	}
	days := 30
	if daysStr := r.URL.Query().Get("days"); daysStr != "" {
		d, err := strconv.Atoi(daysStr)
		if err != nil || d < 1 || d > 365 {
			h.respondV1Validation(w, r, "Invalid days parameter (must be 1-365)")
			return
		}
		days = d
	}
	filter := repository.ReportFilter{WorkspaceID: workspaceID, MilestoneID: milestoneID, StartDate: time.Now().AddDate(0, 0, -days)}
	stats, err := h.summaryRepo.GetOverallStats(filter)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	trend, err := h.summaryRepo.GetTrend(filter)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	failures, err := h.summaryRepo.GetRecentFailures(filter, 20)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	blocked, err := h.summaryRepo.GetRecentBlocked(filter, 20)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, map[string]any{
		"overall": map[string]any{
			"total_runs":  stats.TotalRuns,
			"total_tests": stats.TotalTests,
			"passed":      stats.Passed,
			"failed":      stats.Failed,
			"blocked":     stats.Blocked,
			"skipped":     stats.Skipped,
			"not_run":     stats.NotRun,
			"pass_rate":   stats.PassRate(),
		},
		"trend":           trend,
		"recent_failures": failures,
		"recent_blocked":  blocked,
	})
}

// LinkTestResultItem handles POST /rest/api/v1/workspaces/{workspaceId}/test-results/{resultId}/items
//
// @Summary      Link a work item to a test result
// @Description  Body is `{"item_id":<id>}`. Both entities must live in the same workspace.
// @Tags         tests
// @Accept       json
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int                  true  "Workspace ID"
// @Param        resultId     path      int                  true  "Test result ID"
// @Param        body         body      map[string]int       true  "Work item ID to link"
// @Success      201          "Item linked to test result"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test result or item not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-results/{resultId}/items [post]
func (h *TestManagementHandler) LinkTestResultItem(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	resultID, ok := h.ParsePathID(w, r, "resultId", "test result ID")
	if !ok {
		return
	}
	var data struct {
		ItemID int `json:"item_id"`
	}
	if !h.DecodeBodyOrRespond(w, r, &data) {
		return
	}
	if err := h.runSvc.LinkResultItem(workspaceID, resultID, data.ItemID); err != nil {
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, services.ErrTestRunItemNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondCreated(w, map[string]bool{"success": true})
}

// UnlinkTestResultItem handles DELETE /rest/api/v1/workspaces/{workspaceId}/test-results/{resultId}/items/{itemId}
//
// @Summary      Unlink a work item from a test result
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        resultId     path      int  true  "Test result ID"
// @Param        itemId       path      int  true  "Work item ID"
// @Success      204          "Item unlinked from test result"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace, result, or item ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test result not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-results/{resultId}/items/{itemId} [delete]
func (h *TestManagementHandler) UnlinkTestResultItem(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestExecute)
	if !ok {
		return
	}
	resultID, ok := h.ParsePathID(w, r, "resultId", "test result ID")
	if !ok {
		return
	}
	itemID, ok := h.ParsePathID(w, r, "itemId", "item ID")
	if !ok {
		return
	}
	if err := h.runSvc.UnlinkResultItem(workspaceID, resultID, itemID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondNoContent(w)
}

// ListTestResultItems handles GET /rest/api/v1/workspaces/{workspaceId}/test-results/{resultId}/items
//
// @Summary      List work items linked to a test result
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        resultId     path      int  true  "Test result ID"
// @Success      200          {array}   models.Item
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or result ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test result not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-results/{resultId}/items [get]
func (h *TestManagementHandler) ListTestResultItems(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := h.requireTestWorkspace(w, r, models.PermissionTestView)
	if !ok {
		return
	}
	resultID, ok := h.ParsePathID(w, r, "resultId", "test result ID")
	if !ok {
		return
	}
	items, err := h.runSvc.ListResultItems(workspaceID, resultID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			h.RespondNotFound(w, r)
		} else {
			h.RespondInternalError(w, r)
		}
		return
	}
	h.RespondOK(w, items)
}
