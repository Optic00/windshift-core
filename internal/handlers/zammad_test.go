package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"windshift/internal/database"
	"windshift/internal/integrations/zammad"
	"windshift/internal/logger"
	"windshift/internal/middleware"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
	"windshift/internal/sso"
)

func newZammadHandlerTest(t *testing.T) (*ZammadHandler, database.Database, *models.User, int) {
	t.Helper()
	db, err := database.NewSQLiteDB(t.TempDir() + "/windshift.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecWrite(`INSERT INTO users (email, username, first_name, last_name) VALUES (?, ?, ?, ?)`, "admin@example.test", "synthetic-admin", "Synthetic", "Admin")
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	result, err = db.ExecWrite(`INSERT INTO workspaces (name, key) VALUES (?, ?)`, "Primary", "PRI")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, _ := result.LastInsertId()
	credentialService := services.NewActionCredentialService(repository.NewActionCredentialRepository(db), "synthetic-handler-test-secret")
	service := services.NewZammadService(db, repository.NewZammadRepository(db), credentialService, nil, nil, nil, nil)
	return NewZammadHandler(repository.NewItemRepository(db), service, nil, logger.NewAuditor(db)), db, &models.User{ID: int(userID), Username: "synthetic-admin"}, int(workspaceID)
}

func authenticatedZammadRequest(method, target string, body []byte, user *models.User) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), middleware.ContextKeyUser, user))
}

func TestZammadHandlerCreateConnectionResponseOmitsSecret(t *testing.T) {
	handler, db, user, workspaceID := newZammadHandlerTest(t)
	body, _ := json.Marshal(models.CreateZammadConnectionRequest{
		Slug: "helpdesk", Name: "Synthetic helpdesk", BaseURL: "https://zammad.example.test",
		APIToken: "handler-secret-token", DefaultGroupID: 7, DefaultGroupName: "Support",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{workspaceID},
	})
	recorder := httptest.NewRecorder()
	handler.CreateConnection(recorder, authenticatedZammadRequest(http.MethodPost, "/api/admin/zammad-connections", body, user))
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response status or type: %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["has_api_token"] != true || response["id"] == "" || response["base_url"] != "https://zammad.example.test" {
		t.Fatalf("unexpected response shape: %#v", response)
	}
	if _, exists := response["api_token"]; exists {
		t.Fatalf("response disclosed api_token field: %s", recorder.Body.String())
	}
	if _, exists := response["credential_id"]; exists {
		t.Fatalf("response disclosed credential_id field: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "handler-secret-token") {
		t.Fatalf("response disclosed token: %s", recorder.Body.String())
	}
	var encrypted string
	if err := db.QueryRow("SELECT encrypted_secret FROM action_credentials").Scan(&encrypted); err != nil || encrypted == "handler-secret-token" {
		t.Fatalf("credential was not encrypted: err=%v", err)
	}
	genericRepo := repository.NewIntegrationProviderRepository(db)
	providers, err := genericRepo.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 0 {
		t.Fatalf("provider-specific Zammad connection leaked into generic OAuth CRUD: %#v", providers)
	}
	if err := genericRepo.Delete(response["id"].(string)); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("generic provider delete was not blocked: %v", err)
	}
}

func TestZammadHandlerReturnsStructuredValidationAndNotFoundErrors(t *testing.T) {
	handler, _, user, workspaceID := newZammadHandlerTest(t)
	body, _ := json.Marshal(models.CreateZammadConnectionRequest{
		Slug: "helpdesk", Name: "Synthetic helpdesk", BaseURL: "http://zammad.example.test",
		APIToken: "handler-secret-token", DefaultCustomer: "robot@example.test",
		WorkspaceIDs: []int{workspaceID},
	})
	recorder := httptest.NewRecorder()
	handler.CreateConnection(recorder, authenticatedZammadRequest(http.MethodPost, "/api/admin/zammad-connections", body, user))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected validation status 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var validation map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &validation); err != nil {
		t.Fatal(err)
	}
	if validation["code"] != "VALIDATION_FAILED" {
		t.Fatalf("unexpected validation response: %#v", validation)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/admin/zammad-connections/missing", nil)
	request.SetPathValue("id", "missing")
	recorder = httptest.NewRecorder()
	handler.GetConnection(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected not-found status 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestZammadHandlerMapsRemoteAuthenticationFailureToBadGateway(t *testing.T) {
	handler, _, _, _ := newZammadHandlerTest(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/admin/zammad-connections/example/test", nil)
	if handler.respondServiceError(recorder, request, &zammad.APIError{StatusCode: http.StatusUnauthorized}) {
		t.Fatal("expected handler to write an error response")
	}
	if recorder.Code != http.StatusBadGateway || strings.Contains(recorder.Body.String(), "401") {
		t.Fatalf("unexpected upstream response: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestZammadOAuthCallbackRedirectsToIntegrationsAdminTab(t *testing.T) {
	handler, _, _, _ := newZammadHandlerTest(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/integrations/zammad/oauth/callback?error=access_denied&state=missing", nil)
	handler.OAuthCallback(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/admin/integration-providers?tab=zammad&oauth=error" {
		t.Fatalf("unexpected OAuth callback redirect: status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestZammadOAuthCallbackAuditsInitiatorSuccessAndFailureWithoutSecrets(t *testing.T) {
	handler, db, user, workspaceID := newZammadHandlerTest(t)
	handler.SetOAuthBaseURL("https://windshift.example.test")
	handler.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-handler-test-secret"))
	handler.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, body []byte, _ map[string]string) (*zammad.Response, error) {
		if strings.Contains(string(body), "failure-code") {
			return &zammad.Response{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":"upstream_failure"}`)}, nil
		}
		return &zammad.Response{StatusCode: http.StatusOK, Body: []byte(`{"access_token":"audit-access-secret","refresh_token":"audit-refresh-secret","expires_in":3600}`)}, nil
	}))
	connection, err := handler.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "audit-oauth", Name: "Audit OAuth", BaseURL: "https://audit-oauth.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "audit-client", OAuthClientSecret: "audit-client-secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{workspaceID},
	}, user.ID)
	if err != nil {
		t.Fatal(err)
	}

	callback := func(code string) {
		t.Helper()
		authURL, err := handler.service.StartOAuth(context.Background(), connection.ProviderID, user.ID, "https://windshift.example.test")
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(authURL)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/integrations/zammad/oauth/callback?state="+url.QueryEscape(parsed.Query().Get("state"))+"&code="+url.QueryEscape(code), nil)
		recorder := httptest.NewRecorder()
		handler.OAuthCallback(recorder, request)
		if recorder.Code != http.StatusFound {
			t.Fatalf("callback status = %d", recorder.Code)
		}
	}
	callback("success-code")
	callback("failure-code")

	rows, err := db.Query(`SELECT user_id, username, success, COALESCE(error_message, ''), COALESCE(details, '')
		FROM audit_logs WHERE action_type = ? ORDER BY id`, logger.ActionZammadOAuthCredentialSet)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type auditRow struct {
		userID                int
		username, error, data string
		success               bool
	}
	var audits []auditRow
	for rows.Next() {
		var row auditRow
		if err := rows.Scan(&row.userID, &row.username, &row.success, &row.error, &row.data); err != nil {
			t.Fatal(err)
		}
		audits = append(audits, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 || !audits[0].success || audits[1].success || audits[0].userID != user.ID || audits[1].userID != user.ID || audits[0].username != user.Username || audits[1].error != "oauth_callback_failed" {
		t.Fatalf("unexpected callback audits: %#v", audits)
	}
	for _, audit := range audits {
		if strings.Contains(audit.data, "success-code") || strings.Contains(audit.data, "failure-code") || strings.Contains(audit.data, "audit-access-secret") || strings.Contains(audit.data, "audit-refresh-secret") || strings.Contains(audit.data, "audit-client-secret") {
			t.Fatalf("callback audit disclosed OAuth material: %s", audit.data)
		}
	}
}
