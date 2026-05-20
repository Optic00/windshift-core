package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/services"
)

// TestSCMInjectRefRequest is the body for the test-only inject-ref hook.
// All fields are optional except workspace_repository_id, ref_type,
// and ref_name; the handler fills in sensible defaults
// (ref_short via the same rule the sync layer uses, sha = "deadbeef")
// so tests don't have to hard-code every field.
type TestSCMInjectRefRequest struct {
	WorkspaceRepositoryID int    `json:"workspace_repository_id"`
	RefType               string `json:"ref_type"` // "tag" or "branch"
	RefName               string `json:"ref_name"`
	RefShort              string `json:"ref_short,omitempty"`
	SHA                   string `json:"sha,omitempty"`
	PrevName              string `json:"prev_name,omitempty"`
	RepoFullName          string `json:"repo_full_name,omitempty"`
}

// testSCMInjectRefHandler is the underlying http.Handler. Kept as a
// struct so its deps (db lookup for workspace_id, action service for
// the EmitActionEvent call) are explicit.
type testSCMInjectRefHandler struct {
	db            database.Database
	actionService *services.ActionService
}

// NewTestSCMInjectRef builds the handler. Server.go mounts the
// returned http.Handler only when WINDSHIFT_E2E_TEST_HOOKS=1 — the
// gate lives at the call site so the build never compiles a route
// that would still register itself under prod conditions.
func NewTestSCMInjectRef(db database.Database, as *services.ActionService) http.Handler {
	return &testSCMInjectRefHandler{db: db, actionService: as}
}

func (h *testSCMInjectRefHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[TestSCMInjectRefRequest](w, r)
	if !ok {
		return
	}
	if req.WorkspaceRepositoryID <= 0 || req.RefName == "" || (req.RefType != "tag" && req.RefType != "branch") {
		respondValidationError(w, r, "workspace_repository_id, ref_name, and ref_type (tag|branch) are required")
		return
	}

	// Resolve workspace + repo_name for the payload (the action
	// engine's ref.short/repo.full_name substitution reads from this).
	var workspaceID int
	var repoName sql.NullString
	if err := h.db.QueryRow(`
		SELECT wsc.workspace_id, wr.repository_name
		FROM workspace_repositories wr
		JOIN workspace_scm_connections wsc ON wsc.id = wr.workspace_scm_connection_id
		WHERE wr.id = ?
	`, req.WorkspaceRepositoryID).Scan(&workspaceID, &repoName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondNotFound(w, r, "workspace_repository")
		} else {
			respondInternalError(w, r, err)
		}
		return
	}

	short := req.RefShort
	if short == "" {
		short = defaultRefShort(req.RefType, req.RefName)
	}
	sha := req.SHA
	if sha == "" {
		sha = "deadbeef"
	}
	fullName := req.RepoFullName
	if fullName == "" {
		fullName = repoName.String
	}

	eventType := models.ActionTriggerSCMTagCreated
	if req.RefType == "branch" {
		eventType = models.ActionTriggerSCMReleaseBranchCreated
	}

	values := map[string]interface{}{
		"ref.name":                     req.RefName,
		"ref.short":                    short,
		"ref.sha":                      sha,
		"ref.type":                     req.RefType,
		"ref.prev_name":                req.PrevName,
		"repo.full_name":               fullName,
		"repo.workspace_repository_id": req.WorkspaceRepositoryID,
	}
	h.actionService.EmitActionEvent(&models.ActionEvent{
		EventType:   eventType,
		WorkspaceID: workspaceID,
		NewValues:   values,
	})

	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprintf(w, "{\"queued\":true,\"event_type\":%q}", string(eventType))
}

// TestSetupMockRepoRequest creates the minimal SCM infrastructure a
// Playwright test needs to exercise the milestone-from-tag chain:
// a mock provider row, a workspace SCM connection, and a workspace
// repository. Returns the new IDs so the spec can pass
// workspace_repository_id to /inject-ref. All three rows are
// idempotent (re-call returns the existing rows by deterministic
// slug/name) so tests can re-run without cleanup.
type TestSetupMockRepoRequest struct {
	WorkspaceID    int    `json:"workspace_id"`
	RepositoryName string `json:"repository_name,omitempty"` // default "octo/demo"
}

type TestSetupMockRepoResponse struct {
	ProviderID               int    `json:"provider_id"`
	WorkspaceSCMConnectionID int    `json:"workspace_scm_connection_id"`
	WorkspaceRepositoryID    int    `json:"workspace_repository_id"`
	RepositoryName           string `json:"repository_name"`
}

type testSetupMockRepoHandler struct {
	db database.Database
}

// NewTestSetupMockRepo returns the http.Handler that seeds the
// SCM-side rows. Same env-gate as inject-ref — server.go mounts both
// behind WINDSHIFT_E2E_TEST_HOOKS=1.
func NewTestSetupMockRepo(db database.Database) http.Handler {
	return &testSetupMockRepoHandler{db: db}
}

func (h *testSetupMockRepoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeJSON[TestSetupMockRepoRequest](w, r)
	if !ok {
		return
	}
	if req.WorkspaceID <= 0 {
		respondValidationError(w, r, "workspace_id is required")
		return
	}
	repoName := req.RepositoryName
	if repoName == "" {
		repoName = "octo/demo"
	}

	// Provider — keyed by slug "test-mock". One row per server lifetime.
	var providerID int
	if err := h.db.QueryRow(`SELECT id FROM scm_providers WHERE slug = ?`, "test-mock").Scan(&providerID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			respondInternalError(w, r, err)
			return
		}
		if err := h.db.QueryRow(`
			INSERT INTO scm_providers(slug, name, provider_type, auth_method, enabled)
			VALUES ('test-mock', 'Test Mock SCM', 'github', 'pat', 1)
			RETURNING id
		`).Scan(&providerID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	// Connection — one per (workspace, provider).
	var connID int
	if err := h.db.QueryRow(
		`SELECT id FROM workspace_scm_connections WHERE workspace_id = ? AND scm_provider_id = ?`,
		req.WorkspaceID, providerID,
	).Scan(&connID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			respondInternalError(w, r, err)
			return
		}
		if err := h.db.QueryRow(`
			INSERT INTO workspace_scm_connections(workspace_id, scm_provider_id, enabled)
			VALUES (?, ?, 1)
			RETURNING id
		`, req.WorkspaceID, providerID).Scan(&connID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	// Repository — keyed by (connection, repository_name).
	var repoID int
	if err := h.db.QueryRow(
		`SELECT id FROM workspace_repositories WHERE workspace_scm_connection_id = ? AND repository_name = ?`,
		connID, repoName,
	).Scan(&repoID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			respondInternalError(w, r, err)
			return
		}
		if err := h.db.QueryRow(`
			INSERT INTO workspace_repositories(
				workspace_scm_connection_id, repository_external_id,
				repository_name, repository_url, default_branch, is_active
			) VALUES (?, ?, ?, ?, 'main', 1)
			RETURNING id
		`, connID, "ext-"+repoName, repoName, "https://example.invalid/"+repoName).Scan(&repoID); err != nil {
			respondInternalError(w, r, err)
			return
		}
	}

	respondJSONOK(w, TestSetupMockRepoResponse{
		ProviderID:               providerID,
		WorkspaceSCMConnectionID: connID,
		WorkspaceRepositoryID:    repoID,
		RepositoryName:           repoName,
	})
}

// defaultRefShort mirrors the sync layer's ref-short rules so a test
// that passes ref_name without ref_short still gets the right value.
// Kept here (rather than importing scm) because handlers cannot
// import scm without a layering complaint.
func defaultRefShort(refType, refName string) string {
	switch refType {
	case "tag":
		if len(refName) > 1 && (refName[0] == 'v' || refName[0] == 'V') && refName[1] >= '0' && refName[1] <= '9' {
			return refName[1:]
		}
		return refName
	case "branch":
		const prefix = "release/"
		if len(refName) > len(prefix) && refName[:len(prefix)] == prefix {
			return refName[len(prefix):]
		}
		return refName
	default:
		return refName
	}
}
