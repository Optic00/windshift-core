package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
)

// newRefresherTestDB spins up a temp-file SQLite + the cache table the
// repository writes to. A temp file (not :memory:) is needed because the
// production sqlite wrapper uses separate read and write pools, and an
// in-memory DB is per-connection unless explicitly shared.
func newRefresherTestDB(t *testing.T) database.Database {
	t.Helper()
	path := t.TempDir() + "/cache.db"
	db, err := database.NewSQLiteDB(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecWrite(`CREATE TABLE llm_provider_model_cache (
		provider_type     TEXT PRIMARY KEY,
		models_json       TEXT NOT NULL,
		last_refreshed_at DATETIME,
		last_error        TEXT,
		updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create cache table: %v", err)
	}
	return db
}

func TestModelRefresher_SuccessWritesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-4o","name":"GPT-4o","context_length":128000},
			{"id":"anthropic/claude-3.5-sonnet","name":"Claude 3.5 Sonnet","context_length":200000},
			{"id":"empty-id-skipped","name":""}
		]}`))
	}))
	defer srv.Close()

	db := newRefresherTestDB(t)
	cache := NewModelCache(db)
	refresher := newModelRefresherWithClient(cache, http.DefaultClient)

	provider := ProviderInfo{
		Type:           "openrouter",
		Name:           "OpenRouter",
		BaseURL:        srv.URL,
		ModelsEndpoint: "/models",
	}
	models, err := refresher.Refresh(context.Background(), provider, "")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("want 3 parsed models, got %d", len(models))
	}
	if models[2].Name != models[2].ID {
		t.Errorf("blank Name should fall back to ID, got %q", models[2].Name)
	}

	got, err := cache.Get("openrouter")
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if len(got.Models) != 3 {
		t.Errorf("want 3 cached models, got %d", len(got.Models))
	}
	if got.LastRefreshedAt == nil {
		t.Error("LastRefreshedAt should be set after success")
	}
	if got.LastError != "" {
		t.Errorf("LastError should be empty after success, got %q", got.LastError)
	}
}

func TestModelRefresher_FailurePreservesPriorCache(t *testing.T) {
	db := newRefresherTestDB(t)
	cache := NewModelCache(db)

	// Pre-seed with a prior successful refresh.
	prior := []ModelInfo{{ID: "old/model", Name: "Old Model", MaxTokens: 4096}}
	if err := cache.SaveSuccess("openrouter", prior, time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	refresher := newModelRefresherWithClient(cache, http.DefaultClient)
	provider := ProviderInfo{
		Type:           "openrouter",
		BaseURL:        srv.URL,
		ModelsEndpoint: "/models",
	}
	_, err := refresher.Refresh(context.Background(), provider, "")
	if err == nil {
		t.Fatal("expected error from 503")
	}

	got, err := cache.Get("openrouter")
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "old/model" {
		t.Errorf("prior cached models should survive a failed refresh, got %+v", got.Models)
	}
	if !strings.Contains(got.LastError, "503") && !strings.Contains(got.LastError, "not ready") {
		t.Errorf("LastError should mention the 503, got %q", got.LastError)
	}
}

