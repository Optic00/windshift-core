package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/logger"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/utils"
)

type TestSetHandler struct {
	service *services.TestSetService
	auditor *logger.Auditor
}

func NewTestSetHandlerWithPool(service *services.TestSetService, auditor *logger.Auditor) *TestSetHandler {
	return &TestSetHandler{service: service, auditor: auditor}
}

func (h *TestSetHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrTestSetMilestoneNotFound):
		respondValidationError(w, r, err.Error())
	case errors.Is(err, services.ErrTestSetCaseNotFound):
		respondNotFound(w, r, "test_case")
	case errors.Is(err, repository.ErrNotFound):
		respondNotFound(w, r, "test_set")
	default:
		respondInternalError(w, r, err)
	}
}

func (h *TestSetHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	sets, err := h.service.List(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, sets)
}

func (h *TestSetHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	set, err := h.service.Get(setID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, set)
}

func (h *TestSetHandler) decodeWrite(w http.ResponseWriter, r *http.Request) (int, *models.User, models.TestSet, bool) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, nil, models.TestSet{}, false
	}
	set, ok := decodeJSON[models.TestSet](w, r)
	if !ok {
		return 0, nil, models.TestSet{}, false
	}
	return workspaceID, utils.GetCurrentUser(r), set, true
}

func (h *TestSetHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, input, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	set, err := h.service.Create(workspaceID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if user != nil {
		h.auditor.Log(r, user, logger.ActionTestSetCreate, logger.ResourceTestSet, &set.ID, set.Name)
	}
	respondJSONCreated(w, set)
}

func (h *TestSetHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, user, input, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	setID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	set, err := h.service.Update(setID, workspaceID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if user != nil {
		h.auditor.Log(r, user, logger.ActionTestSetUpdate, logger.ResourceTestSet, &setID, set.Name)
	}
	respondJSONOK(w, set)
}

func (h *TestSetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	user := utils.GetCurrentUser(r)
	if err := h.service.Delete(setID, workspaceID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if user != nil {
		h.auditor.Log(r, user, logger.ActionTestSetDelete, logger.ResourceTestSet, &setID, "")
	}
	w.WriteHeader(http.StatusNoContent)
}

func requireTestSetPath(w http.ResponseWriter, r *http.Request) (workspaceID, setID int, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, 0, false
	}
	setID, ok = requireIDParam(w, r, "id")
	return workspaceID, setID, ok
}

func (h *TestSetHandler) GetTestCases(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	testCases, err := h.service.ListCases(setID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, testCases)
}

func (h *TestSetHandler) AddTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	var request struct {
		TestCaseID int `json:"test_case_id"`
	}
	if err := newJSONDecoder(w, r).Decode(&request); err != nil {
		respondBadRequest(w, r, "Invalid request body")
		return
	}
	if err := h.service.AddCase(setID, request.TestCaseID, workspaceID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *TestSetHandler) RemoveTestCase(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	testCaseID, ok := requireIDParam(w, r, "testCaseId")
	if !ok {
		return
	}
	if err := h.service.RemoveCase(setID, testCaseID, workspaceID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TestSetHandler) GetRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID, setID, ok := requireTestSetPath(w, r)
	if !ok {
		return
	}
	runs, err := h.service.ListRuns(setID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, runs)
}
