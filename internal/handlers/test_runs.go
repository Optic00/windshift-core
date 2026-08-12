package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/logger"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type TestRunHandler struct {
	service *services.TestRunService
	auditor *logger.Auditor
}

// TestRunDetailResponse is the complete read model shared by execution and
// completed-run detail screens.
type TestRunDetailResponse = services.TestRunDetail

func NewTestRunHandlerWithPool(
	service *services.TestRunService,
	auditor *logger.Auditor,
) *TestRunHandler {
	return &TestRunHandler{
		service: service,
		auditor: auditor,
	}
}

func (h *TestRunHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	// Build filters from query params
	filters := services.TestRunListFilters{
		IncludeEnded: true, // By default show all runs
	}

	assigneeFilter := r.URL.Query().Get("assignee_id")
	if assigneeFilter == "unassigned" {
		filters.Unassigned = true
	} else if assigneeFilter != "" {
		assigneeID, _ := strconv.Atoi(assigneeFilter)
		filters.AssigneeID = &assigneeID
	}

	runs, err := h.service.List(workspaceID, filters)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, runs)
}

func (h *TestRunHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	run, err := h.service.GetByID(id, workspaceID)
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "test_run")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	respondJSONOK(w, run)
}

// GetDetail returns the run, its case/step snapshot, and all result rows in a
// single frontend request without per-case repository queries.
func (h *TestRunHandler) GetDetail(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	detail, err := h.service.GetDetail(runID, workspaceID)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "test_run")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, detail)
}

func (h *TestRunHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	user := utils.GetCurrentUser(r)

	var input struct {
		Name       string `json:"name"`
		TemplateID int    `json:"template_id"`
		SetID      int    `json:"set_id"`
		AssigneeID *int   `json:"assignee_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondValidationError(w, r, "Invalid JSON")
		return
	}

	run, err := h.service.Create(workspaceID, services.TestRunCreateRequest{
		Name:       input.Name,
		TemplateID: input.TemplateID,
		SetID:      input.SetID,
		AssigneeID: input.AssigneeID,
	})
	if err != nil {
		respondValidationError(w, r, err.Error())
		return
	}

	h.auditor.Log(r, user, logger.ActionTestRunCreate, logger.ResourceTestRun, &run.ID, run.Name)

	respondJSONCreated(w, run)
}

func (h *TestRunHandler) End(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.Complete(id, workspaceID); err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "test_run")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
}

// Update updates a test run (supports updating assignee)
func (h *TestRunHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	id, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	user := utils.GetCurrentUser(r)

	var input struct {
		Name       string `json:"name"`
		AssigneeID *int   `json:"assignee_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondValidationError(w, r, "Invalid JSON")
		return
	}

	_, err := h.service.Update(id, workspaceID, services.TestRunUpdateRequest{
		Name:       input.Name,
		AssigneeID: input.AssigneeID,
	})
	if err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "test_run")
		} else {
			respondValidationError(w, r, err.Error())
		}
		return
	}

	h.auditor.Log(r, user, logger.ActionTestRunUpdate, logger.ResourceTestRun, &id, "")

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// requireTestRunAccess parses workspaceID and runID from path params and
// verifies the test run belongs to the workspace.
func (h *TestRunHandler) requireTestRunAccess(w http.ResponseWriter, r *http.Request) (workspaceID, runID int, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, 0, false
	}

	runID, ok = requireIDParam(w, r, "id")
	if !ok {
		return 0, 0, false
	}

	exists, existsErr := h.service.Exists(runID, workspaceID)
	if existsErr != nil {
		respondInternalError(w, r, existsErr)
		return 0, 0, false
	}
	if !exists {
		respondNotFound(w, r, "test_run")
		return 0, 0, false
	}

	return workspaceID, runID, true
}

func (h *TestRunHandler) GetResults(w http.ResponseWriter, r *http.Request) {
	workspaceID, runID, ok := h.requireTestRunAccess(w, r)
	if !ok {
		return
	}

	results, err := h.service.ListResults(runID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, results)
}

func (h *TestRunHandler) UpdateResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	resultID, ok := requireIDParam(w, r, "resultId")
	if !ok {
		return
	}

	var input struct {
		Status       string `json:"status"`
		ActualResult string `json:"actual_result"`
		Notes        string `json:"notes"`
	}
	if err := newJSONDecoder(w, r).Decode(&input); err != nil {
		respondValidationError(w, r, "Invalid JSON")
		return
	}

	if _, err := h.service.UpdateResult(workspaceID, runID, resultID, services.TestResultUpdateRequest{
		Status:       input.Status,
		ActualResult: input.ActualResult,
		Notes:        input.Notes,
	}); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "test_result")
			return
		}
		respondValidationError(w, r, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *TestRunHandler) GetBySet(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	setID, ok := requireIDParam(w, r, "setId")
	if !ok {
		return
	}

	// Use service to filter by set
	runs, err := h.service.List(workspaceID, services.TestRunListFilters{
		SetID:        &setID,
		IncludeEnded: true,
	})
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, runs)
}

// UpdateStepResult updates or creates a step result for a test execution
func (h *TestRunHandler) UpdateStepResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	runID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}

	stepID, ok := requireIDParam(w, r, "stepId")
	if !ok {
		return
	}

	var update struct {
		Status       string `json:"status"`
		ActualResult string `json:"actual_result"`
		Notes        string `json:"notes"`
		ItemID       *int   `json:"item_id,omitempty"`
	}
	if err := newJSONDecoder(w, r).Decode(&update); err != nil {
		respondValidationError(w, r, "Invalid JSON")
		return
	}

	if err := h.service.UpdateStepResult(workspaceID, runID, stepID, services.TestStepResultUpdateRequest{
		Status: update.Status, ActualResult: update.ActualResult, Notes: update.Notes, ItemID: update.ItemID,
	}); err != nil {
		switch {
		case errors.Is(err, services.ErrTestRunItemNotFound):
			respondNotFound(w, r, "item")
		case errors.Is(err, repository.ErrNotFound):
			respondNotFound(w, r, "test_result")
		case errors.Is(err, services.ErrInvalidTestResultStatus):
			respondValidationError(w, r, err.Error())
		default:
			respondInternalError(w, r, err)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// GetStepResults returns all step results for a test run
func (h *TestRunHandler) GetStepResults(w http.ResponseWriter, r *http.Request) {
	workspaceID, runID, ok := h.requireTestRunAccess(w, r)
	if !ok {
		return
	}

	stepResults, err := h.service.ListStepResults(runID, workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, stepResults)
}

// Delete removes a test run and all associated results
func (h *TestRunHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, id, user, ok := requireWorkspaceIDAndID(w, r)
	if !ok {
		return
	}

	if err := h.service.Delete(id, workspaceID); err != nil {
		if err == repository.ErrNotFound {
			respondNotFound(w, r, "test_run")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	h.auditor.Log(r, user, logger.ActionTestRunDelete, logger.ResourceTestRun, &id, "")

	w.WriteHeader(http.StatusOK)
}

// LinkItemToTestResult links a work item to a test result
func (h *TestRunHandler) LinkItemToTestResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	resultID, ok := requireIDParam(w, r, "resultId")
	if !ok {
		return
	}

	var data struct {
		ItemID int `json:"item_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&data); err != nil {
		respondValidationError(w, r, "Invalid JSON")
		return
	}

	if err := h.service.LinkResultItem(workspaceID, resultID, data.ItemID); errors.Is(err, services.ErrTestRunItemNotFound) {
		respondNotFound(w, r, "item")
		return
	} else if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "test_result")
		return
	} else if err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// UnlinkItemFromTestResult removes item link from test result
func (h *TestRunHandler) UnlinkItemFromTestResult(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	resultID, ok := requireIDParam(w, r, "resultId")
	if !ok {
		return
	}

	itemID, ok := requireIDParam(w, r, "itemId")
	if !ok {
		return
	}

	if err := h.service.UnlinkResultItem(workspaceID, resultID, itemID); errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "test_result")
		return
	} else if err != nil {
		respondInternalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetTestResultItems gets all linked items for a test result
func (h *TestRunHandler) GetTestResultItems(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}

	resultID, ok := requireIDParam(w, r, "resultId")
	if !ok {
		return
	}

	items, err := h.service.ListResultItems(workspaceID, resultID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			respondNotFound(w, r, "test_result")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	respondJSONOK(w, items)
}
