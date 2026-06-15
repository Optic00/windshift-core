package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"windshift/internal/repository"
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
// struct so its SCM hook service dependency is explicit.
type testSCMInjectRefHandler struct {
	svc *services.TestSCMHookService
}

// NewTestSCMInjectRef builds the handler. Server.go mounts the
// returned http.Handler only when WINDSHIFT_E2E_TEST_HOOKS=1 — the
// gate lives at the call site so the build never compiles a route
// that would still register itself under prod conditions.
func NewTestSCMInjectRef(svc *services.TestSCMHookService) http.Handler {
	return &testSCMInjectRefHandler{svc: svc}
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

	eventType, err := h.svc.InjectRef(services.TestSCMInjectRefParams{
		WorkspaceRepositoryID: req.WorkspaceRepositoryID,
		RefType:               req.RefType,
		RefName:               req.RefName,
		RefShort:              req.RefShort,
		SHA:                   req.SHA,
		PrevName:              req.PrevName,
		RepoFullName:          req.RepoFullName,
	})
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "workspace_repository")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

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
	svc *services.TestSCMHookService
}

// NewTestSetupMockRepo returns the http.Handler that seeds the
// SCM-side rows. Same env-gate as inject-ref — server.go mounts both
// behind WINDSHIFT_E2E_TEST_HOOKS=1.
func NewTestSetupMockRepo(svc *services.TestSCMHookService) http.Handler {
	return &testSetupMockRepoHandler{svc: svc}
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

	result, err := h.svc.SetupMockRepo(req.WorkspaceID, req.RepositoryName)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	respondJSONOK(w, TestSetupMockRepoResponse{
		ProviderID:               result.ProviderID,
		WorkspaceSCMConnectionID: result.WorkspaceSCMConnectionID,
		WorkspaceRepositoryID:    result.WorkspaceRepositoryID,
		RepositoryName:           result.RepositoryName,
	})
}
