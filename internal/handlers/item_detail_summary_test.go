package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"windshift/internal/models"
)

func TestItemDetailSummaryRequiresAuthentication(t *testing.T) {
	handler := NewItemDetailHandler(&ItemHandler{}, nil, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/items/42/detail-summary", nil)
	request.SetPathValue("id", "42")
	recorder := httptest.NewRecorder()

	handler.Get(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestKeyItemDetailSummaryRequiresAuthenticationBeforeResolution(t *testing.T) {
	handler := NewItemDetailHandler(&ItemHandler{}, nil, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/workspaces/WI/items/42/detail-summary", nil)
	request.SetPathValue("key", "WI")
	request.SetPathValue("number", "42")
	recorder := httptest.NewRecorder()

	handler.GetByKeyAndNumber(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestResolveItemDetailScreenIDMatchesFrontendFallbackChain(t *testing.T) {
	defaultCreate, defaultEdit, typeCreate, typeView := 10, 11, 20, 21
	itemTypeID := 7
	config := &models.ConfigurationSet{
		CreateScreenID: &defaultCreate,
		EditScreenID:   &defaultEdit,
		ItemTypeConfigs: []models.ItemTypeConfig{
			{ItemTypeID: itemTypeID, CreateScreenID: &typeCreate, ViewScreenID: &typeView},
		},
	}

	if got := resolveItemDetailScreenID(config, &itemTypeID, "view", 1); got != typeView {
		t.Fatalf("view screen = %d, want item-type view %d", got, typeView)
	}
	if got := resolveItemDetailScreenID(config, &itemTypeID, "edit", 1); got != typeCreate {
		t.Fatalf("edit screen = %d, want item-type create fallback %d", got, typeCreate)
	}
	otherType := 8
	if got := resolveItemDetailScreenID(config, &otherType, "edit", 1); got != defaultEdit {
		t.Fatalf("default edit screen = %d, want %d", got, defaultEdit)
	}
	if got := resolveItemDetailScreenID(nil, &itemTypeID, "view", 1); got != 1 {
		t.Fatalf("hard fallback screen = %d, want 1", got)
	}
}
