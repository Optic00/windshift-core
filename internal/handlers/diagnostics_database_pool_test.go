package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"windshift/internal/database"
	"windshift/internal/repository"
)

func TestDiagnosticsDatabasePool(t *testing.T) {
	db, err := database.NewSQLiteDBWithPoolSizes("file:diagnostics-pool?mode=memory&cache=shared", 4, 1)
	if err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	h := &DiagnosticsHandler{databaseDiagRepo: repository.NewDatabaseDiagnosticsRepository(db)}
	recorder := httptest.NewRecorder()
	h.GetDatabasePool(recorder, httptest.NewRequest("GET", "/api/admin/diagnostics/database-pool", nil))

	if recorder.Code != 200 {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Pool DatabasePoolSnapshot `json:"pool"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Pool.Driver != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", response.Pool.Driver)
	}
	if response.Pool.MaxOpenConnections != 4 {
		t.Fatalf("max open connections = %d, want 4", response.Pool.MaxOpenConnections)
	}
	if response.Pool.SaturationThresholdPct != databasePoolSaturationThresholdPercent {
		t.Fatalf("threshold = %d, want %d", response.Pool.SaturationThresholdPct, databasePoolSaturationThresholdPercent)
	}
}
