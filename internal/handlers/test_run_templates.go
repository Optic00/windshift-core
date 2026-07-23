package handlers

import (
	"errors"
	"net/http"

	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

type TestRunTemplateHandler struct {
	service *services.TestRunTemplateService
}

func NewTestRunTemplateHandlerWithPool(service *services.TestRunTemplateService) *TestRunTemplateHandler {
	return &TestRunTemplateHandler{service: service}
}

func (h *TestRunTemplateHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrTestRunTemplateSetNotFound):
		respondNotFound(w, r, "test_set")
	case errors.Is(err, repository.ErrNotFound):
		respondNotFound(w, r, "test_run_template")
	default:
		respondInternalError(w, r, err)
	}
}

func (h *TestRunTemplateHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return
	}
	templates, err := h.service.List(workspaceID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, templates)
}

func (h *TestRunTemplateHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID, templateID, ok := requireTestRunTemplatePath(w, r)
	if !ok {
		return
	}
	template, err := h.service.Get(templateID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, template)
}

func (h *TestRunTemplateHandler) decodeWrite(w http.ResponseWriter, r *http.Request) (int, models.TestRunTemplate, bool) {
	workspaceID, ok := requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, models.TestRunTemplate{}, false
	}
	template, ok := decodeJSON[models.TestRunTemplate](w, r)
	return workspaceID, template, ok
}

func (h *TestRunTemplateHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID, input, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	template, err := h.service.Create(workspaceID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONCreated(w, template)
}

func (h *TestRunTemplateHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID, input, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	templateID, ok := requireIDParam(w, r, "id")
	if !ok {
		return
	}
	template, err := h.service.Update(templateID, workspaceID, input)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, template)
}

func (h *TestRunTemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID, templateID, ok := requireTestRunTemplatePath(w, r)
	if !ok {
		return
	}
	if err := h.service.Delete(templateID, workspaceID); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TestRunTemplateHandler) GetExecutions(w http.ResponseWriter, r *http.Request) {
	workspaceID, templateID, ok := requireTestRunTemplatePath(w, r)
	if !ok {
		return
	}
	runs, err := h.service.ListExecutions(templateID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONOK(w, runs)
}

func (h *TestRunTemplateHandler) Execute(w http.ResponseWriter, r *http.Request) {
	workspaceID, templateID, ok := requireTestRunTemplatePath(w, r)
	if !ok {
		return
	}
	run, err := h.service.Execute(templateID, workspaceID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	respondJSONCreated(w, run)
}

func requireTestRunTemplatePath(w http.ResponseWriter, r *http.Request) (workspaceID, templateID int, ok bool) {
	workspaceID, ok = requireIDParam(w, r, "workspaceId")
	if !ok {
		return 0, 0, false
	}
	templateID, ok = requireIDParam(w, r, "id")
	return workspaceID, templateID, ok
}
