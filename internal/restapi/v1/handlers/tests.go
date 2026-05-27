// Package handlers — v1 test-management surface (WI-68 Phase 1).
//
// This file exposes the read + run-lifecycle endpoints `ws test` and
// MCP need on /rest/api/v1:
//
//   - test cases (list / get / steps)
//   - test sets   (list / get / cases-in-set)
//   - test runs   (list / get / create / end / results / update-result)
//   - test run templates (execute)
//
// Mutating the test catalog (case CRUD, set CRUD, folders, labels,
// reports, result↔item linking) stays cookie-only until a follow-up
// ticket lifts the rest. The handlers here reuse the existing service
// + repository layer; only the HTTP surface is new.
//
// Permission model: token-scope gating happens at the route layer
// (tests:read / tests:write). The handler additionally checks the
// caller's workspace test permission (test.view / test.execute) via
// PermissionService — a token with tests:write still can't drive runs
// in a workspace where its user lacks test.execute.
//
// Response shape: the legacy cookie handlers emit the resource directly
// (`respondJSONOK(w, payload)`), and we keep that for parity — v1 list
// endpoints conventionally use {"items":[...]}, but the existing CLI
// client and MCP tools expect bare arrays. Wrapping here would break
// both before WI-78 ships the test-half repoint.
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/restapi"
	"windshift/internal/services"
	"windshift/internal/utils"
)

// TestManagementHandler bundles the read + run-lifecycle endpoints into
// one handler so the route layer wires a single dependency. The
// services / repos it wraps are the same ones the cookie surface uses.
type TestManagementHandler struct {
	BaseHandler
	caseSvc          *services.TestCaseService
	runSvc           *services.TestRunService
	setRepo          *repository.TestSetRepository
	runRepo          *repository.TestRunRepository
	runTemplateRepo  *repository.TestRunTemplateRepository
	workspaceChecker *repository.WorkspaceResourceRepository
}

// NewTestManagementHandler wires the v1 test-management handler. db /
// permissionService come from the v1 router; the rest is plumbing.
func NewTestManagementHandler(db database.Database, permissionService *services.PermissionService) *TestManagementHandler {
	return &TestManagementHandler{
		BaseHandler:      NewBaseHandler(db, permissionService),
		caseSvc:          services.NewTestCaseService(db),
		runSvc:           services.NewTestRunService(db),
		setRepo:          repository.NewTestSetRepository(db),
		runRepo:          repository.NewTestRunRepository(db),
		runTemplateRepo:  repository.NewTestRunTemplateRepository(db),
		workspaceChecker: repository.NewWorkspaceResourceRepository(db),
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

// testResultWithCaseTitle matches the cookie GetResults response so MCP
// and `ws test result` keep working unchanged after the repoint.
type testResultWithCaseTitle struct {
	models.TestResult
	TestCaseTitle string `json:"test_case_title"`
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
	user, ok := h.RequireAuth(w, r)
	if !ok {
		return 0, false
	}
	workspaceID, ok = h.ParsePathID(w, r, "workspaceId", "workspace ID")
	if !ok {
		return 0, false
	}
	allowed, err := h.PermissionService.HasWorkspacePermission(user.ID, workspaceID, permission)
	if err != nil || !allowed {
		h.RespondNotFound(w, r)
		return 0, false
	}
	return workspaceID, true
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
	}
	if folderIDParam := r.URL.Query().Get("folder_id"); folderIDParam != "" && folderIDParam != "null" {
		folderID, err := strconv.Atoi(folderIDParam)
		if err != nil {
			h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid folder_id"))
			return
		}
		params.FolderID = &folderID
	}

	cases, err := h.caseSvc.List(params)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, cases)
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
	// Confirm the case belongs to the workspace so steps from another
	// workspace can't be fetched by knowing a step ID alone.
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
	sets, err := h.setRepo.FindAllWithStats(workspaceID)
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
	set, err := h.setRepo.FindByID(id, workspaceID)
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
	cases, err := h.setRepo.FindTestCases(setID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondOK(w, cases)
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
	req.Name = utils.SanitizeTitle(req.Name)
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
// @Success      200          {array}   handlers.testResultWithCaseTitle
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
	exists, err := h.runSvc.Exists(runID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !exists {
		h.RespondNotFound(w, r)
		return
	}
	rows, err := h.runRepo.FindResultsWithTestCase(runID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	results := make([]testResultWithCaseTitle, 0, len(rows))
	for _, row := range rows {
		results = append(results, testResultWithCaseTitle{
			TestResult:    row.TestResult,
			TestCaseTitle: row.TestCaseTitle,
		})
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
	exists, err := h.runSvc.Exists(runID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	if !exists {
		h.RespondNotFound(w, r)
		return
	}
	var req testResultUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.RespondError(w, r, restapi.NewAPIError(http.StatusBadRequest, restapi.ErrCodeInvalidInput, "Invalid request body"))
		return
	}
	// Match the cookie surface's XSS handling: SanitizeDescription
	// preserves the <br /> markers MilkdownEditor emits for blank lines.
	req.ActualResult = utils.SanitizeDescription(req.ActualResult)
	req.Notes = utils.SanitizeDescription(req.Notes)
	if err := h.runSvc.UpdateResult(runID, resultID, services.TestResultUpdateRequest{
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
	template, err := h.runTemplateRepo.FindCore(templateID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		h.RespondNotFound(w, r)
		return
	}
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	runCount, err := h.runTemplateRepo.CountExecutions(templateID, workspaceID)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	runName := template.Name + " - Run " + strconv.Itoa(runCount+1)
	run, err := h.runTemplateRepo.Execute(workspaceID, templateID, template.SetID, runName)
	if err != nil {
		h.RespondInternalError(w, r)
		return
	}
	h.RespondCreated(w, run)
}
