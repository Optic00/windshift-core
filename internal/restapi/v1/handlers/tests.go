// Package handlers — v1 test-management surface (WI-68 + WI-81).
//
// This file exposes the cookie test-management surface on /rest/api/v1:
// folders, cases, steps, labels, sets/plans, run templates, runs, reports,
// and result↔item links. The handlers reuse the existing service /
// repository / cookie-handler layer where possible; only the bearer-token
// HTTP surface and scope checks are new.
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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/contextkeys"
	"windshift/internal/database"
	legacyhandlers "windshift/internal/handlers"
	"windshift/internal/logger"
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
	legacy           legacyTestManagementHandlers
}

type legacyTestManagementHandlers struct {
	folder      *legacyhandlers.TestFolderHandler
	caseHandler *legacyhandlers.TestCaseHandler
	set         *legacyhandlers.TestSetHandler
	runTemplate *legacyhandlers.TestRunTemplateHandler
	run         *legacyhandlers.TestRunHandler
	summary     *legacyhandlers.TestSummaryHandler
}

// NewTestManagementHandler wires the v1 test-management handler. db /
// permissionService come from the v1 router; the rest is plumbing.
func NewTestManagementHandler(db database.Database, permissionService *services.PermissionService) *TestManagementHandler {
	caseSvc := services.NewTestCaseService(db)
	runSvc := services.NewTestRunService(db)
	setRepo := repository.NewTestSetRepository(db)
	runRepo := repository.NewTestRunRepository(db)
	runTemplateRepo := repository.NewTestRunTemplateRepository(db)
	workspaceChecker := repository.NewWorkspaceResourceRepository(db)
	auditor := logger.NewAuditor(db)

	return &TestManagementHandler{
		BaseHandler:      NewBaseHandler(db, permissionService),
		caseSvc:          caseSvc,
		runSvc:           runSvc,
		setRepo:          setRepo,
		runRepo:          runRepo,
		runTemplateRepo:  runTemplateRepo,
		workspaceChecker: workspaceChecker,
		legacy: legacyTestManagementHandlers{
			folder:      legacyhandlers.NewTestFolderHandlerWithPool(db),
			caseHandler: legacyhandlers.NewTestCaseHandlerWithPool(caseSvc, auditor),
			set:         legacyhandlers.NewTestSetHandlerWithPool(setRepo, workspaceChecker, auditor),
			runTemplate: legacyhandlers.NewTestRunTemplateHandlerWithPool(runTemplateRepo, workspaceChecker),
			run:         legacyhandlers.NewTestRunHandlerWithPool(runSvc, runRepo, repository.NewItemRepository(db), auditor),
			summary:     legacyhandlers.NewTestSummaryHandlerWithPool(repository.NewTestSummaryRepository(db)),
		},
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

func (h *TestManagementHandler) serveLegacy(w http.ResponseWriter, r *http.Request, permission string, next http.HandlerFunc) {
	_, user, ok := h.requireTestWorkspaceUser(w, r, permission)
	if !ok {
		return
	}

	// The cookie handlers reuse the same repository / service code and response
	// shapes. Project the bearer-authenticated user into the legacy context key
	// so audit logging via utils.GetCurrentUser continues to work.
	ctx := context.WithValue(r.Context(), contextkeys.User, user)
	next(w, r.Clone(ctx))
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

// --- phase 2 delegated routes (WI-81) ---

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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.folder.GetAllFolders)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.folder.CreateFolder)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.folder.GetFolder)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.folder.UpdateFolder)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.folder.DeleteFolder)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.folder.ReorderFolders)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.CreateTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.UpdateTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.DeleteTestCase)
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
// @Param        body         body      map[string]interface{}  true  "Folder ID + sort order"
// @Success      200          "Test case moved"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id}/move [put]
func (h *TestManagementHandler) MoveTestCase(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.MoveTestCase)
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
// @Param        body         body      map[string]interface{}  true  "Folder ID + test case IDs in desired order"
// @Success      200          "Test cases reordered"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.manage"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/reorder [put]
func (h *TestManagementHandler) ReorderTestCases(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.ReorderTestCases)
}

// GetTestCaseConnections handles GET /rest/api/v1/workspaces/{workspaceId}/test-cases/{id}/connections
//
// @Summary      List related sets, templates, and executions for a test case
// @Tags         tests
// @Produce      json
// @Security     BearerAuth
// @Param        workspaceId  path      int  true  "Workspace ID"
// @Param        id           path      int  true  "Test case ID"
// @Success      200          {object}  map[string]interface{}
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or case ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test case not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-cases/{id}/connections [get]
func (h *TestManagementHandler) GetTestCaseConnections(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.caseHandler.GetTestCaseConnections)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.CreateTestStep)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.UpdateTestStep)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.DeleteTestStep)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.ReorderTestSteps)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.caseHandler.GetAllTestLabels)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.CreateTestLabel)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.UpdateTestLabel)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.DeleteTestLabel)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.caseHandler.GetTestCaseLabels)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.AddTestCaseLabel)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.caseHandler.RemoveTestCaseLabel)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Create)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Update)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Delete)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.AddTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.RemoveTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.set.GetRuns)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.set.GetAll)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Create)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.set.Get)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Update)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.Delete)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.set.GetTestCases)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.AddTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.set.RemoveTestCase)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.set.GetRuns)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.runTemplate.GetAll)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.runTemplate.Create)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.runTemplate.Get)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.runTemplate.Update)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.runTemplate.Delete)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.runTemplate.GetExecutions)
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
// @Param        body         body      map[string]interface{}  true  "Test run fields to update (name, assignee_id)"
// @Success      200          "Test run updated"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body or validation error"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id} [put]
func (h *TestManagementHandler) UpdateTestRun(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestExecute, h.legacy.run.Update)
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
	h.serveLegacy(w, r, models.PermissionTestManage, h.legacy.run.Delete)
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
// @Success      200          {object}  map[string]interface{}
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid workspace or run ID"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/steps [get]
func (h *TestManagementHandler) GetTestRunStepResults(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.run.GetStepResults)
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
// @Param        body         body      map[string]interface{}  true  "Step result update"
// @Success      200          "Step result recorded"
// @Failure      400          {object}  handlers.ErrorResponse  "Invalid request body"
// @Failure      401          {object}  handlers.ErrorResponse
// @Failure      403          {object}  handlers.ErrorResponse  "Token lacks the tests:write scope"
// @Failure      404          {object}  handlers.ErrorResponse  "Test run, step, or item not found in this workspace"
// @Failure      500          {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-runs/{id}/steps/{stepId} [put]
func (h *TestManagementHandler) UpdateTestRunStepResult(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestExecute, h.legacy.run.UpdateStepResult)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.summary.GetMarkdownSummary)
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
// @Success      200           {object}  map[string]interface{}
// @Failure      400           {object}  handlers.ErrorResponse  "Invalid query parameters"
// @Failure      401           {object}  handlers.ErrorResponse
// @Failure      403           {object}  handlers.ErrorResponse  "Token lacks the tests:read scope"
// @Failure      404           {object}  handlers.ErrorResponse  "Workspace not found or caller lacks test.view"
// @Failure      500           {object}  handlers.ErrorResponse
// @Router       /workspaces/{workspaceId}/test-reports/summary [get]
func (h *TestManagementHandler) GetTestReportsSummary(w http.ResponseWriter, r *http.Request) {
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.summary.GetReportsSummary)
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
	h.serveLegacy(w, r, models.PermissionTestExecute, h.legacy.run.LinkItemToTestResult)
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
	h.serveLegacy(w, r, models.PermissionTestExecute, h.legacy.run.UnlinkItemFromTestResult)
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
	h.serveLegacy(w, r, models.PermissionTestView, h.legacy.run.GetTestResultItems)
}
