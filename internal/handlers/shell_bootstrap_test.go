package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"windshift/internal/contextkeys"
	"windshift/internal/models"
)

func TestShellBootstrapRequiresAuthentication(t *testing.T) {
	handler := NewShellBootstrapHandler(nil, nil, nil, nil, nil, nil)
	recorder := httptest.NewRecorder()

	handler.Get(recorder, httptest.NewRequest(http.MethodGet, "/api/shell-bootstrap", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestShellBootstrapComposesFeatureSnapshot(t *testing.T) {
	handler := NewShellBootstrapHandler(
		NewFeaturesHandler(nil, true, true),
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/shell-bootstrap", nil)
	request = request.WithContext(context.WithValue(request.Context(), contextkeys.User, &models.User{ID: 7}))

	handler.Get(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response ShellBootstrapResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Features.SSHAvailable || !response.Features.LogbookAvailable {
		t.Fatalf("feature snapshot = %+v", response.Features)
	}
	if response.AttachmentStatus == nil || response.AttachmentStatus.Enabled {
		t.Fatalf("attachment status = %+v", response.AttachmentStatus)
	}
}
