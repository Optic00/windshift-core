package services

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/models"
	"windshift/internal/repository"
)

type selectiveAssetPermissionChecker struct {
	allowed map[int]bool
}

func (c selectiveAssetPermissionChecker) HasAssetSetPermission(userID, _ int, _ string) (bool, error) {
	return c.allowed[userID], nil
}

func TestActionServicesRejectDisabledActions(t *testing.T) {
	t.Parallel()

	if err := (&ActionService{}).executeAction(&models.Action{ID: 4}, nil, nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("core action error = %v, want disabled", err)
	}
	if err := (&AssetActionService{}).executeAction(&models.AssetAction{ID: 5}, nil, nil); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("asset action error = %v, want disabled", err)
	}
}

func TestAssetActionMutationsFailClosedWithoutPermissionDependencies(t *testing.T) {
	t.Parallel()

	service := &AssetActionService{}
	ctx := &models.AssetActionExecutionContext{Event: &models.AssetActionEvent{
		SetID:       9,
		AssetID:     10,
		ActorUserID: 11,
	}}
	step := &models.StepResult{}
	if err := service.executeNode(&models.AssetActionNode{NodeType: models.AssetNodeSetField}, ctx, step); err == nil || !strings.Contains(err.Error(), "permission checker not configured") {
		t.Fatalf("set_field error = %v, want missing asset permission checker", err)
	}
	createItemConfig := `{"workspace_id":12,"item_type_id":1,"title":"test"}`
	if err := service.executeNode(&models.AssetActionNode{NodeType: models.AssetNodeCreateItem, NodeConfig: createItemConfig}, ctx, step); err == nil || !strings.Contains(err.Error(), "permission service not configured") {
		t.Fatalf("create_item error = %v, want missing workspace permission service", err)
	}
}

func TestAssetActionNotificationsTargetOnlyAuthorizedConfiguredUsers(t *testing.T) {
	t.Parallel()

	manager := &recordingNotificationManager{}
	notifications := &NotificationService{notificationManager: manager}
	service := &AssetActionService{
		notificationService: notifications,
		assetPermChecker: selectiveAssetPermissionChecker{allowed: map[int]bool{
			21: true,
			22: false,
		}},
	}
	ctx := &models.AssetActionExecutionContext{Event: &models.AssetActionEvent{
		SetID:       7,
		AssetID:     8,
		ActorUserID: 20,
	}, Variables: map[string]interface{}{}}
	step := &models.StepResult{}
	node := &models.AssetActionNode{
		NodeType:   models.AssetNodeNotifyUser,
		NodeConfig: `{"recipients":["21","22"],"title":"Asset changed","message":"Review it","include_link":true}`,
	}
	if err := service.executeNotifyUser(node, ctx, step); err != nil {
		t.Fatalf("executeNotifyUser: %v", err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.batches) != 1 || len(manager.batches[0]) != 1 {
		t.Fatalf("notification batches = %+v, want one authorized recipient", manager.batches)
	}
	got := manager.batches[0][0]
	if got.UserID != 21 || got.ActionURL != "/assets/8" {
		t.Fatalf("notification = %+v, want user 21 and asset link", got)
	}
}

func TestNormalizeCredentialWorkspaceIDsRejectsInvalidIDs(t *testing.T) {
	t.Parallel()

	if _, err := normalizeCredentialWorkspaceIDs([]int{1, 0, 2}); err == nil {
		t.Fatal("workspace ID 0 was silently ignored")
	}
	got, err := normalizeCredentialWorkspaceIDs([]int{3, 3, 4})
	if err != nil {
		t.Fatalf("normalizeCredentialWorkspaceIDs: %v", err)
	}
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("normalized IDs = %v, want [3 4]", got)
	}
}

func TestBuildHTTPHeadersRejectsCaseInsensitiveDuplicates(t *testing.T) {
	t.Parallel()

	service := &ActionService{}
	_, err := service.buildHTTPHeadersWithCredentials(context.Background(), &models.HTTPClientConfig{
		DefaultHeaders: map[string]string{"X-Trace": "one", " x-trace ": "two"},
	}, nil, 1, 2)
	if err == nil {
		t.Fatal("case-insensitive duplicate default headers unexpectedly succeeded")
	}

	_, err = service.buildHTTPHeadersWithCredentials(context.Background(), &models.HTTPClientConfig{}, map[string]string{
		"X-Trace": "one",
		"x-trace": "two",
	}, 1, 2)
	if err == nil {
		t.Fatal("case-insensitive duplicate request headers unexpectedly succeeded")
	}
}

func TestBuildHTTPHeadersAllowsCallerToOverrideNonSecretDefault(t *testing.T) {
	t.Parallel()

	service := &ActionService{}
	got, err := service.buildHTTPHeadersWithCredentials(context.Background(), &models.HTTPClientConfig{
		DefaultHeaders: map[string]string{"Content-Type": "application/json"},
	}, map[string]string{"content-type": "text/plain"}, 1, 2)
	if err != nil {
		t.Fatalf("buildHTTPHeadersWithCredentials: %v", err)
	}
	if got["Content-Type"] != "text/plain" || len(got) != 1 {
		t.Fatalf("merged headers = %#v, want one overridden Content-Type", got)
	}
}

func TestRedirectStripsCredentialHeadersOnAnyCrossOriginHop(t *testing.T) {
	t.Parallel()

	client := newSSRFSafeClient(time.Second, []string{"https://**"})
	original := httptest.NewRequest(http.MethodGet, "https://api.example.test/start", nil)
	original.Header.Set("X-API-Key", "secret")
	original.Header.Set("X-Signature", "signature")
	original.Header.Set("X-Trace-ID", "safe")

	crossOrigin := httptest.NewRequest(http.MethodGet, "https://redirect.example.test/next", nil)
	crossOrigin.Header = original.Header.Clone()
	if err := client.CheckRedirect(crossOrigin, []*http.Request{original}); err != nil {
		t.Fatalf("CheckRedirect cross-origin: %v", err)
	}
	if crossOrigin.Header.Get("X-API-Key") != "" || crossOrigin.Header.Get("X-Signature") != "" {
		t.Fatalf("sensitive headers survived cross-origin redirect: %#v", crossOrigin.Header)
	}
	if crossOrigin.Header.Get("X-Trace-ID") != "safe" {
		t.Fatalf("non-sensitive header was stripped: %#v", crossOrigin.Header)
	}

	backToOrigin := httptest.NewRequest(http.MethodGet, "https://api.example.test/final", nil)
	backToOrigin.Header = original.Header.Clone()
	if err := client.CheckRedirect(backToOrigin, []*http.Request{original, crossOrigin}); err != nil {
		t.Fatalf("CheckRedirect return-to-origin: %v", err)
	}
	if backToOrigin.Header.Get("X-API-Key") != "" {
		t.Fatal("credential header was restored after a cross-origin redirect hop")
	}
}

func TestRedirectPreservesCredentialHeadersWithinOrigin(t *testing.T) {
	t.Parallel()

	client := newSSRFSafeClient(time.Second, []string{"https://api.example.test/**"})
	original := httptest.NewRequest(http.MethodGet, "https://api.example.test/start", nil)
	redirect := httptest.NewRequest(http.MethodGet, "https://api.example.test/next", nil)
	redirect.Header.Set("X-API-Key", "secret")
	if err := client.CheckRedirect(redirect, []*http.Request{original}); err != nil {
		t.Fatalf("CheckRedirect: %v", err)
	}
	if redirect.Header.Get("X-API-Key") != "secret" {
		t.Fatal("same-origin redirect stripped credential header")
	}
}

func TestActionHTTPDiagnosticURLRedactionDropsQueryFragmentAndUserInfo(t *testing.T) {
	t.Parallel()

	got := redactHTTPURLForDiagnostics("https://user:password@example.test/hook?token=plaintext&customer=private#fragment")
	if strings.Contains(got, "password") || strings.Contains(got, "plaintext") || strings.Contains(got, "private") || strings.Contains(got, "fragment") {
		t.Fatalf("diagnostic URL leaked confidential values: %q", got)
	}
	if !strings.Contains(got, "example.test/hook") {
		t.Fatalf("diagnostic URL lost useful endpoint context: %q", got)
	}
}

func TestActionHTTPResponsePreviewRedactsBeforeTruncating(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", 700)
	preview := truncateString(RedactString(`{"token":"`+secret+`"}`), 500)
	if strings.Contains(preview, secret[:20]) {
		t.Fatal("response preview leaked a long JSON credential")
	}
}

func TestCreateAssetActionEnforcesSetSchemaAndDefaultStatus(t *testing.T) {
	db, err := database.NewSQLiteDB(filepath.Join(t.TempDir(), "create-asset-action.db"))
	if err != nil {
		t.Fatalf("NewSQLiteDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	insertID := func(query string, args ...interface{}) int {
		t.Helper()
		var id int
		if err := db.QueryRow(query, args...).Scan(&id); err != nil {
			t.Fatalf("insert fixture: %v", err)
		}
		return id
	}
	actorID := insertID(`
		INSERT INTO users (email, username, first_name, last_name)
		VALUES ('action-asset@example.test', 'action-asset', 'Action', 'Asset') RETURNING id
	`)
	setID := insertID(`INSERT INTO asset_management_sets (name, created_by) VALUES ('Action assets', ?) RETURNING id`, actorID)
	otherSetID := insertID(`INSERT INTO asset_management_sets (name, created_by) VALUES ('Other assets', ?) RETURNING id`, actorID)
	typeID := insertID(`INSERT INTO asset_types (set_id, name) VALUES (?, 'Server') RETURNING id`, setID)
	otherTypeID := insertID(`INSERT INTO asset_types (set_id, name) VALUES (?, 'Other server') RETURNING id`, otherSetID)
	defaultStatusID := insertID(`INSERT INTO asset_statuses (set_id, name, is_default) VALUES (?, 'Available', true) RETURNING id`, setID)
	otherStatusID := insertID(`INSERT INTO asset_statuses (set_id, name, is_default) VALUES (?, 'Other', true) RETURNING id`, otherSetID)
	otherCategoryID := insertID(`INSERT INTO asset_categories (set_id, name) VALUES (?, 'Other category') RETURNING id`, otherSetID)
	fieldID := insertID(`INSERT INTO custom_field_definitions (name, field_type) VALUES ('Serial number', 'text') RETURNING id`)
	if _, err := db.ExecWrite(`
		INSERT INTO asset_type_fields (asset_type_id, custom_field_id, is_required)
		VALUES (?, ?, true)
	`, typeID, fieldID); err != nil {
		t.Fatalf("link required field: %v", err)
	}

	service := &ActionService{
		db:               db,
		itemRepo:         repository.NewItemRepository(db),
		assetPermChecker: selectiveAssetPermissionChecker{allowed: map[int]bool{actorID: true}},
	}
	ctx := &models.ExecutionContext{
		Event:            &models.ActionEvent{},
		EffectiveActorID: actorID,
		Variables:        map[string]interface{}{},
	}
	execute := func(config string) error {
		t.Helper()
		return service.executeCreateAsset(&models.ActionNode{NodeConfig: config}, ctx, &models.StepResult{})
	}

	if err := execute(fmt.Sprintf(`{"asset_set_id":%d,"asset_type_id":%d,"title":"Server"}`, setID, otherTypeID)); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-set asset type error = %v, want ownership rejection", err)
	}
	if err := execute(fmt.Sprintf(`{"asset_set_id":%d,"asset_type_id":%d,"category_id":%d,"title":"Server"}`, setID, typeID, otherCategoryID)); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-set category error = %v, want ownership rejection", err)
	}
	if err := execute(fmt.Sprintf(`{"asset_set_id":%d,"asset_type_id":%d,"status_id":%d,"title":"Server"}`, setID, typeID, otherStatusID)); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("cross-set status error = %v, want ownership rejection", err)
	}
	if err := execute(fmt.Sprintf(`{"asset_set_id":%d,"asset_type_id":%d,"title":"Server"}`, setID, typeID)); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing required custom field error = %v, want schema rejection", err)
	}

	validConfig := fmt.Sprintf(`{
		"asset_set_id":%d,
		"asset_type_id":%d,
		"title":"Server",
		"field_mappings":[{"source_type":"literal","source_value":"rack-7","target_field_id":"%d"}]
	}`, setID, typeID, fieldID)
	if err := execute(validConfig); err != nil {
		t.Fatalf("valid create_asset action: %v", err)
	}

	var gotStatusID, gotCreatorID int
	var gotFields string
	if err := db.QueryRow(`SELECT status_id, created_by, custom_field_values FROM assets`).Scan(&gotStatusID, &gotCreatorID, &gotFields); err != nil {
		t.Fatalf("load created asset: %v", err)
	}
	if gotStatusID != defaultStatusID {
		t.Fatalf("status_id = %d, want default %d", gotStatusID, defaultStatusID)
	}
	if gotCreatorID != actorID {
		t.Fatalf("created_by = %d, want effective actor %d", gotCreatorID, actorID)
	}
	if !strings.Contains(gotFields, "rack-7") {
		t.Fatalf("custom_field_values = %q, want mapped required field", gotFields)
	}
}
