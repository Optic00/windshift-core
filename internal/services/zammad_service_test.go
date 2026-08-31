package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"windshift/internal/database"
	"windshift/internal/integrations/zammad"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/sso"
	"windshift/internal/utils"
)

type fakeZammadTransport struct {
	mu                    sync.Mutex
	requests              int
	posts                 int
	puts                  int
	ticket                map[string]any
	postErrorAfterCreate  error
	postStatusAfterCreate int
	putError              error
	getStatus             int
	getTicket             map[string]any
	hideSearch            bool
	groups                []map[string]any
	states                []map[string]any
	users                 []map[string]any
	putPayloads           []map[string]any
}

func (f *fakeZammadTransport) Do(_ context.Context, method, targetURL string, body []byte, headers map[string]string) (*zammad.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if headers["Authorization"] != "Token token=synthetic-zammad-token" {
		return nil, errors.New("unexpected authorization header")
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}
	switch {
	case parsed.Path == "/api/v1/tickets/search":
		if f.ticket == nil || f.hideSearch {
			return jsonResponse(http.StatusOK, []any{}), nil
		}
		return jsonResponse(http.StatusOK, []any{f.ticket}), nil
	case parsed.Path == "/api/v1/tickets" && method == http.MethodPost:
		f.posts++
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		f.ticket = map[string]any{
			"id": 901, "number": "420901", "group_id": 7,
			"state_id": 2, "state": "open",
			"windshift_item_key": payload["windshift_item_key"],
		}
		if f.postErrorAfterCreate != nil {
			err := f.postErrorAfterCreate
			f.postErrorAfterCreate = nil
			return nil, err
		}
		if f.postStatusAfterCreate != 0 {
			status := f.postStatusAfterCreate
			f.postStatusAfterCreate = 0
			return jsonResponse(status, map[string]string{"error": "synthetic create failure"}), nil
		}
		return jsonResponse(http.StatusCreated, f.ticket), nil
	case strings.HasPrefix(parsed.Path, "/api/v1/tickets/") && method == http.MethodPut:
		f.puts++
		if f.putError != nil {
			return nil, f.putError
		}
		payload := map[string]any{}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
		f.putPayloads = append(f.putPayloads, payload)
		if f.ticket != nil {
			for key, value := range payload {
				f.ticket[key] = value
			}
		}
		return jsonResponse(http.StatusOK, f.ticket), nil
	case strings.HasPrefix(parsed.Path, "/api/v1/tickets/"):
		if f.getStatus != 0 {
			return jsonResponse(f.getStatus, map[string]string{"error": "synthetic remote detail"}), nil
		}
		if f.getTicket != nil {
			return jsonResponse(http.StatusOK, f.getTicket), nil
		}
		return jsonResponse(http.StatusOK, f.ticket), nil
	case parsed.Path == "/api/v1/groups":
		if f.groups != nil {
			return jsonResponse(http.StatusOK, f.groups), nil
		}
		return jsonResponse(http.StatusOK, []map[string]any{{"id": 7, "name": "Support", "active": true}}), nil
	case parsed.Path == "/api/v1/ticket_states":
		if f.states != nil {
			return jsonResponse(http.StatusOK, f.states), nil
		}
		return jsonResponse(http.StatusOK, []map[string]any{{"id": 2, "name": "open", "active": true}, {"id": 4, "name": "closed", "active": true}}), nil
	case parsed.Path == "/api/v1/users/search":
		return jsonResponse(http.StatusOK, f.users), nil
	case parsed.Path == "/api/v1/object_manager_attributes":
		return jsonResponse(http.StatusOK, []map[string]any{{"name": "windshift_item_key", "object": "Ticket", "active": true}}), nil
	default:
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"}), nil
	}
}

func (f *fakeZammadTransport) counts() (requests, posts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.posts
}

func (f *fakeZammadTransport) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts
}

func (f *fakeZammadTransport) lastPutPayload() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.putPayloads) == 0 {
		return nil
	}
	return f.putPayloads[len(f.putPayloads)-1]
}

func (f *fakeZammadTransport) resetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = 0
}

func jsonResponse(status int, value any) *zammad.Response {
	body, _ := json.Marshal(value)
	return &zammad.Response{StatusCode: status, Body: body}
}

type allowZammadPermission struct{}

func (allowZammadPermission) HasWorkspacePermission(_, _ int, _ string) (bool, error) {
	return true, nil
}

type fakeZammadWorkflow struct {
	db     database.Database
	writes int
}

func (f *fakeZammadWorkflow) PerformTransition(_ context.Context, req PerformTransitionRequest, itemRepo *repository.ItemRepository, _ *ConditionService, _ transitionApprovalService) (*PerformTransitionResult, error) {
	item, err := itemRepo.FindByIDWithDetails(req.ItemID)
	if err != nil {
		return nil, err
	}
	if item.StatusID != nil && *item.StatusID == req.ToStatusID {
		return &PerformTransitionResult{Item: item, OldStatusID: item.StatusID, NewStatusID: item.StatusID, NoOp: true}, nil
	}
	oldStatusID := item.StatusID
	if _, err := f.db.ExecWrite("UPDATE items SET status_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", req.ToStatusID, req.ItemID); err != nil {
		return nil, err
	}
	f.writes++
	item, err = itemRepo.FindByIDWithDetails(req.ItemID)
	if err != nil {
		return nil, err
	}
	newStatusID := req.ToStatusID
	return &PerformTransitionResult{Item: item, OldStatusID: oldStatusID, NewStatusID: &newStatusID}, nil
}

type zammadServiceFixture struct {
	t           *testing.T
	db          database.Database
	service     *ZammadService
	credentials *ActionCredentialService
	transport   *fakeZammadTransport
	connection  *models.ZammadConnection
	workspace1  int
	workspace2  int
	item1       int
	item2       int
	actorID     int
	openStatus  int
	doneStatus  int
}

func newZammadServiceFixture(t *testing.T, workflow zammadWorkflowTransitioner) *zammadServiceFixture {
	t.Helper()
	db, err := database.NewSQLiteDB(t.TempDir() + "/windshift.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Initialize(); err != nil {
		t.Fatal(err)
	}
	f := &zammadServiceFixture{t: t, db: db, transport: &fakeZammadTransport{}}
	f.actorID = mustInsertID(t, db, `INSERT INTO users (email, username, first_name, last_name) VALUES (?, ?, ?, ?)`, "agent@example.test", "zammad-agent", "Zammad", "Agent")
	f.workspace1 = mustInsertID(t, db, `INSERT INTO workspaces (name, key) VALUES (?, ?)`, "Primary", "PRI")
	f.workspace2 = mustInsertID(t, db, `INSERT INTO workspaces (name, key) VALUES (?, ?)`, "Other", "OTH")
	if err := db.QueryRow("SELECT id FROM statuses ORDER BY id LIMIT 1").Scan(&f.openStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id FROM statuses ORDER BY id DESC LIMIT 1").Scan(&f.doneStatus); err != nil {
		t.Fatal(err)
	}
	f.item1 = mustInsertID(t, db, `INSERT INTO items (workspace_id, workspace_item_number, title, description, frac_index, status_id, creator_id, last_active_at) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, f.workspace1, 49, "Synthetic ticket source", "Synthetic description", "a0", f.openStatus, f.actorID)
	f.item2 = mustInsertID(t, db, `INSERT INTO items (workspace_id, workspace_item_number, title, description, frac_index, status_id, creator_id, last_active_at) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, f.workspace2, 1, "Out of scope", "Synthetic description", "a1", f.openStatus, f.actorID)
	credentialService := NewActionCredentialService(repository.NewActionCredentialRepository(db), "synthetic-server-secret-for-zammad-tests")
	f.credentials = credentialService
	if workflow == nil {
		workflow = &fakeZammadWorkflow{db: db}
	}
	f.service = NewZammadService(db, repository.NewZammadRepository(db), credentialService, allowZammadPermission{}, workflow, nil, nil)
	f.service.SetTransportForTesting(f.transport)
	f.connection, err = f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "helpdesk", Name: "Synthetic helpdesk", BaseURL: "https://zammad.example.test",
		APIToken: "synthetic-zammad-token", DefaultGroupID: 7, DefaultGroupName: "Support",
		DefaultCustomer: "robot@example.test", CorrelationField: "windshift_item_key",
		WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func mustInsertID(t *testing.T, db database.Database, query string, args ...any) int {
	t.Helper()
	result, err := db.ExecWrite(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return int(id)
}

func TestZammadCreateTicketIsIdempotentAndPersistsGenericLink(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	first, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	_, posts := f.transport.counts()
	if posts != 1 || first.TicketID != 901 || second.ID != first.ID {
		t.Fatalf("unexpected idempotency result: first=%#v second=%#v posts=%d", first, second, posts)
	}
	var genericID string
	if err := f.db.QueryRow("SELECT item_integration_link_id FROM zammad_ticket_links WHERE id = ?", first.ID).Scan(&genericID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM item_integration_links WHERE id = ? AND external_id = ?", genericID, strconv.Itoa(first.TicketID)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("generic link was not persisted: count=%d err=%v", count, err)
	}
}

func TestZammadLinkExistingTicketPinsExactTicketAndIsIdempotent(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.ticket = map[string]any{
		"id": 711, "number": "420711", "group_id": 7, "group": "Support",
		"state_id": 2, "state": "open", "owner_id": 99,
	}
	f.transport.users = []map[string]any{{"id": 99, "active": true, "firstname": "Grace", "lastname": "Hopper", "group_ids": map[string][]string{"7": {"full"}}}}

	first, err := f.service.LinkExistingTicket(context.Background(), f.item1, f.actorID, models.LinkZammadTicketRequest{
		ConnectionID: f.connection.ProviderID, TicketNumber: "420711",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.LinkExistingTicket(context.Background(), f.item1, f.actorID, models.LinkZammadTicketRequest{
		ConnectionID: f.connection.ProviderID, TicketNumber: "420711",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.TicketID != 711 || first.SyncState != models.ZammadSyncLinked || f.transport.putCount() != 1 {
		t.Fatalf("existing-ticket link was not idempotent: first=%#v second=%#v puts=%d", first, second, f.transport.putCount())
	}
	if first.OwnerID != 99 || first.OwnerName != "Grace Hopper" {
		t.Fatalf("existing-ticket owner was not resolved: %#v", first)
	}
	if got := f.transport.lastPutPayload()["windshift_item_key"]; got != first.CorrelationKey {
		t.Fatalf("remote correlation was not pinned to the local link: %#v", f.transport.lastPutPayload())
	}
}

func TestZammadLinkExistingTicketRejectsDisallowedGroupAndConflictingCorrelation(t *testing.T) {
	t.Run("disallowed group", func(t *testing.T) {
		f := newZammadServiceFixture(t, nil)
		f.transport.ticket = map[string]any{"id": 712, "number": "420712", "group_id": 99, "state_id": 2, "state": "open"}
		_, err := f.service.LinkExistingTicket(context.Background(), f.item1, f.actorID, models.LinkZammadTicketRequest{ConnectionID: f.connection.ProviderID, TicketNumber: "420712"})
		var validationErr *ZammadValidationError
		if !errors.As(err, &validationErr) || f.transport.putCount() != 0 {
			t.Fatalf("disallowed ticket group was accepted: err=%v puts=%d", err, f.transport.putCount())
		}
	})
	t.Run("conflicting correlation", func(t *testing.T) {
		f := newZammadServiceFixture(t, nil)
		f.transport.ticket = map[string]any{
			"id": 713, "number": "420713", "group_id": 7, "state_id": 2, "state": "open",
			"windshift_item_key": "windshift:other-connection:PRI-49",
		}
		_, err := f.service.LinkExistingTicket(context.Background(), f.item1, f.actorID, models.LinkZammadTicketRequest{ConnectionID: f.connection.ProviderID, TicketNumber: "420713"})
		var validationErr *ZammadValidationError
		if !errors.As(err, &validationErr) || f.transport.putCount() != 0 {
			t.Fatalf("conflicting remote correlation was accepted: err=%v puts=%d", err, f.transport.putCount())
		}
		var links int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_ticket_links").Scan(&links); err != nil || links != 0 {
			t.Fatalf("conflicting ticket left a local link behind: links=%d err=%v", links, err)
		}
	})
}

func TestZammadLinkExistingTicketDoesNotStealAlreadyReservedRemoteTicket(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.ticket = map[string]any{"id": 714, "number": "420714", "group_id": 7, "state_id": 2, "state": "open"}
	first, err := f.service.LinkExistingTicket(context.Background(), f.item1, f.actorID, models.LinkZammadTicketRequest{ConnectionID: f.connection.ProviderID, TicketNumber: "420714"})
	if err != nil {
		t.Fatal(err)
	}
	secondItem := mustInsertID(t, f.db, `INSERT INTO items (workspace_id, workspace_item_number, title, description, frac_index, status_id, creator_id, last_active_at) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, f.workspace1, 50, "Second ticket source", "Synthetic description", "a2", f.openStatus, f.actorID)
	// Simulate a remote-side correlation edit between attempts. The local unique
	// provider/ticket reservation must still prevent the second item from taking
	// over this ticket.
	f.transport.ticket["windshift_item_key"] = ""
	puts := f.transport.putCount()
	_, err = f.service.LinkExistingTicket(context.Background(), secondItem, f.actorID, models.LinkZammadTicketRequest{ConnectionID: f.connection.ProviderID, TicketNumber: "420714"})
	if !errors.Is(err, repository.ErrDuplicateEntry) || f.transport.putCount() != puts {
		t.Fatalf("second item stole an already reserved ticket: err=%v puts=%d", err, f.transport.putCount())
	}
	stored, err := f.service.GetTicketLink(first.ID)
	if err != nil || stored.ItemID != f.item1 {
		t.Fatalf("original local reservation changed: link=%#v err=%v", stored, err)
	}
}

func TestZammadUpdateTicketLinkValidatesOwnerForEffectiveGroupAndPersistsSnapshot(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.ticket = map[string]any{"id": 901, "number": "420901", "group_id": 7, "state_id": 2, "owner_id": 1}
	f.transport.groups = []map[string]any{{"id": 7, "name": "Support", "active": true}, {"id": 8, "name": "Escalations", "active": true}}
	f.transport.states = []map[string]any{{"id": 2, "name": "open", "active": true}, {"id": 4, "name": "closed", "active": true}}
	f.transport.users = []map[string]any{{"id": 99, "active": true, "firstname": "Grace", "lastname": "Hopper", "group_ids": map[string][]string{"8": {"full"}}}}
	allowedGroups := []int{7, 8}
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{AllowedGroupIDs: &allowedGroups}); err != nil {
		t.Fatal(err)
	}
	stateID, groupID, ownerID := 4, 8, 99
	updated, err := f.service.UpdateTicketLink(context.Background(), link.ID, models.UpdateZammadTicketLinkRequest{StateID: &stateID, GroupID: &groupID, OwnerID: &ownerID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastStatusID != 4 || updated.LastStatusName != "closed" || updated.GroupID != 8 || updated.GroupName != "Escalations" || updated.OwnerID != 99 || updated.OwnerName != "Grace Hopper" {
		t.Fatalf("remote update snapshot was not persisted: %#v", updated)
	}
	puts := f.transport.putCount()
	invalidOwner := 100
	_, err = f.service.UpdateTicketLink(context.Background(), link.ID, models.UpdateZammadTicketLinkRequest{OwnerID: &invalidOwner})
	var validationErr *ZammadValidationError
	if !errors.As(err, &validationErr) || f.transport.putCount() != puts {
		t.Fatalf("owner without group access reached remote update: err=%v puts=%d", err, f.transport.putCount())
	}
}

func TestZammadUnlinkClearsOnlyExactRemoteCorrelationWithoutDeletingTicket(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.UnlinkTicket(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	if f.transport.ticket == nil || f.transport.ticket["id"] != 901 || f.transport.ticket["windshift_item_key"] != "" {
		t.Fatalf("unlink changed or deleted the remote ticket: %#v", f.transport.ticket)
	}
	if _, err := f.service.GetTicketLink(link.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unlink retained local association: %v", err)
	}
}

func TestZammadUnlinkUpstreamFailurePreservesLocalLink(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.putError = context.DeadlineExceeded
	if _, err := f.service.UnlinkTicket(context.Background(), link.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected remote unlink failure, got %v", err)
	}
	if stored, err := f.service.GetTicketLink(link.ID); err != nil || stored.CorrelationKey != link.CorrelationKey {
		t.Fatalf("unlink failure removed or corrupted the local link: link=%#v err=%v", stored, err)
	}
}

func TestZammadUnlinkDoesNotClearAnotherCorrelation(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.ticket["windshift_item_key"] = "windshift:another-link:OTHER-8"
	puts := f.transport.putCount()
	if _, err := f.service.UnlinkTicket(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	if f.transport.putCount() != puts || f.transport.ticket["windshift_item_key"] != "windshift:another-link:OTHER-8" {
		t.Fatalf("unlink cleared a foreign correlation: %#v", f.transport.ticket)
	}
	if _, err := f.service.GetTicketLink(link.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("stale local link was not removed: %v", err)
	}
}

func TestZammadCreateTicketMarksLocalCompletionFailureUncertainAndKeepsCorrelationPinned(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	if _, err := f.db.ExecWrite(`CREATE TRIGGER fail_zammad_ticket_completion
		BEFORE UPDATE OF ticket_id ON zammad_ticket_links
		WHEN NEW.ticket_id IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'synthetic completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err == nil {
		t.Fatal("expected local completion failure")
	}
	link, err := f.service.TicketLinksForItem(f.item1)
	if err != nil || len(link) != 1 || link[0].SyncState != models.ZammadSyncUncertain || f.transport.ticket["windshift_item_key"] != link[0].CorrelationKey {
		t.Fatalf("known remote ticket was not preserved as uncertain: links=%#v ticket=%#v err=%v", link, f.transport.ticket, err)
	}
	if _, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID}); err == nil {
		t.Fatal("expected retry to surface the retained local completion failure")
	}
	_, posts := f.transport.counts()
	if posts != 1 {
		t.Fatalf("retry created a second ticket after local completion failure: posts=%d", posts)
	}
}

func TestZammadCreateTicketMarksAmbiguousHTTPFailuresUncertainWithoutRetryingPOST(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newZammadServiceFixture(t, nil)
			f.transport.hideSearch = true
			f.transport.postStatusAfterCreate = status

			_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
			var apiErr *zammad.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != status {
				t.Fatalf("expected HTTP %d create failure, got %v", status, err)
			}
			links, err := f.service.TicketLinksForItem(f.item1)
			if err != nil || len(links) != 1 || links[0].SyncState != models.ZammadSyncUncertain {
				t.Fatalf("ambiguous HTTP %d result was not retained as uncertain: links=%#v err=%v", status, links, err)
			}

			link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
			if err != nil {
				t.Fatalf("normal retry after ambiguous HTTP %d failed: %v", status, err)
			}
			_, posts := f.transport.counts()
			if posts != 1 || link.SyncState != models.ZammadSyncUncertain {
				t.Fatalf("normal retry after ambiguous HTTP %d sent another POST: link=%#v posts=%d", status, link, posts)
			}
		})
	}
}

func TestZammadCreateTicketMarksRejectingHTTP400Failed(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.hideSearch = true
	f.transport.postStatusAfterCreate = http.StatusBadRequest

	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	var apiErr *zammad.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 create failure, got %v", err)
	}
	links, err := f.service.TicketLinksForItem(f.item1)
	if err != nil || len(links) != 1 || links[0].SyncState != models.ZammadSyncFailed {
		t.Fatalf("rejecting HTTP 400 result was not retained as failed: links=%#v err=%v", links, err)
	}
}

func TestZammadCreateTicketRejectsCorrelationMatchInDisallowedGroup(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.ticket = map[string]any{
		"id": 715, "number": "420715", "group_id": 99, "state_id": 2, "state": "open",
		"windshift_item_key": "windshift:" + f.connection.ProviderID + ":PRI-49",
	}

	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	var validationErr *ZammadValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("correlation match in a disallowed group was accepted: %v", err)
	}
	links, err := f.service.TicketLinksForItem(f.item1)
	if err != nil || len(links) != 1 || links[0].TicketID != 0 || links[0].SyncState == models.ZammadSyncLinked {
		t.Fatalf("disallowed correlation match completed a local link: links=%#v err=%v", links, err)
	}
	var genericLinks int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM item_integration_links").Scan(&genericLinks); err != nil || genericLinks != 0 {
		t.Fatalf("disallowed correlation match created a generic link: count=%d err=%v", genericLinks, err)
	}
}

func TestZammadSyncPersistsRemoteGroupAndOwner(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 8, "state_id": 3, "state": "pending", "owner_id": 99}
	f.transport.groups = []map[string]any{{"id": 7, "name": "Support", "active": true}, {"id": 8, "name": "Escalations", "active": true}}
	f.transport.users = []map[string]any{{"id": 99, "active": true, "firstname": "Grace", "lastname": "Hopper", "group_ids": map[string][]string{"8": {"full"}}}}
	allowedGroups := []int{7, 8}
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{AllowedGroupIDs: &allowedGroups}); err != nil {
		t.Fatal(err)
	}
	synced, err := f.service.SyncTicketLink(context.Background(), link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if synced.GroupID != 8 || synced.GroupName != "Escalations" || synced.OwnerID != 99 || synced.OwnerName != "Grace Hopper" || synced.LastSyncedAt == nil {
		t.Fatalf("sync did not retain remote group/owner state: %#v", synced)
	}
}

func TestZammadSyncRejectsTicketMovedToDisallowedGroupBeforeSnapshotOrCompletion(t *testing.T) {
	workflow := &fakeZammadWorkflow{}
	f := newZammadServiceFixture(t, workflow)
	workflow.db = f.db
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	allowedGroups := []int{7}
	closedStates := []int{4}
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{
		AllowedGroupIDs:    &allowedGroups,
		ClosedStateIDs:     &closedStates,
		CompletionStatusID: &f.doneStatus,
	}); err != nil {
		t.Fatal(err)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 99, "state_id": 4, "state": "closed"}
	f.transport.groups = []map[string]any{{"id": 7, "name": "Support", "active": true}, {"id": 99, "name": "Restricted", "active": true}}

	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err == nil {
		t.Fatal("sync accepted a ticket moved to a disallowed group")
	}
	stored, err := f.service.GetTicketLink(link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LastStatusID != link.LastStatusID || stored.GroupID != link.GroupID || stored.CompletionApplied || workflow.writes != 0 {
		t.Fatalf("disallowed remote group changed local snapshot or completed item: link=%#v writes=%d", stored, workflow.writes)
	}
	var statusID int
	if err := f.db.QueryRow("SELECT status_id FROM items WHERE id = ?", f.item1).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != f.openStatus {
		t.Fatalf("disallowed remote group completed the item: status=%d", statusID)
	}
}

func TestZammadDueLinksRespectRetryDelayAndOAuthReauthorization(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	oldSync := time.Now().Add(-10 * time.Minute)
	if _, err := f.db.ExecWrite("UPDATE zammad_ticket_links SET last_synced_at = ? WHERE id = ?", oldSync, link.ID); err != nil {
		t.Fatal(err)
	}
	attemptedAt := time.Now()
	if err := f.service.repo.UpdateTicketLinkSync(link.ID, link.LastStatusID, link.LastStatusName,
		link.GroupID, link.GroupName, link.OwnerID, link.OwnerName,
		"synthetic safe failure", attemptedAt, false, false); err != nil {
		t.Fatal(err)
	}
	due, err := f.service.repo.ListDueTicketLinks(time.Now().Add(-2*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("failed link ignored retry delay: %#v", due)
	}
	if _, err := f.db.ExecWrite("UPDATE zammad_ticket_links SET next_attempt_at = ? WHERE id = ?", time.Now().Add(-time.Minute), link.ID); err != nil {
		t.Fatal(err)
	}
	due, err = f.service.repo.ListDueTicketLinks(time.Now().Add(-2*time.Minute), 10)
	if err != nil || len(due) != 1 || due[0].ID != link.ID {
		t.Fatalf("eligible retry was not scheduled: due=%#v err=%v", due, err)
	}
	if _, err := f.db.ExecWrite("UPDATE zammad_connections SET auth_method = 'oauth' WHERE provider_id = ?", f.connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecWrite(`INSERT INTO zammad_oauth_tokens(provider_id, oauth_generation, expires_at, reauthorization_required)
		VALUES (?, 1, ?, true)`, f.connection.ProviderID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	due, err = f.service.repo.ListDueTicketLinks(time.Now().Add(-2*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("reauthorization-required OAuth connection remained due: %#v", due)
	}
}

func TestZammadRetryAfterAmbiguousTimeoutFindsExistingTicket(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.postErrorAfterCreate = context.DeadlineExceeded
	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	_, posts := f.transport.counts()
	if posts != 1 || link.TicketID != 901 || link.SyncState != models.ZammadSyncLinked {
		t.Fatalf("retry created a duplicate or failed to recover: link=%#v posts=%d", link, posts)
	}
}

func TestZammadAmbiguousCreateNeverPostsAgainWhileSearchIsEmpty(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.transport.hideSearch = true
	f.transport.postErrorAfterCreate = context.DeadlineExceeded
	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	_, posts := f.transport.counts()
	if posts != 1 || link.SyncState != models.ZammadSyncUncertain {
		t.Fatalf("uncertain retry sent another POST: state=%s posts=%d", link.SyncState, posts)
	}
	f.transport.postErrorAfterCreate = nil
	link, err = f.service.RetryUncertainTicketCreation(context.Background(), link.ID, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	_, posts = f.transport.counts()
	if posts != 2 || link.TicketID != 901 {
		t.Fatalf("explicit administrator override did not retry creation: ticket=%d posts=%d", link.TicketID, posts)
	}
}

func TestZammadRejectsUnconfiguredGroupBeforeCreate(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	_, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID, GroupID: 99})
	var validationErr *ZammadValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected group validation error, got %v", err)
	}
	_, posts := f.transport.counts()
	if posts != 0 {
		t.Fatalf("unconfigured group reached ticket create endpoint %d times", posts)
	}
}

func TestZammadWorkspaceScopeBlocksHTTPAndSecretIsEncrypted(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	_, err := f.service.CreateTicket(context.Background(), f.item2, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if !errors.Is(err, ErrCredentialScopeMismatch) {
		t.Fatalf("expected scope mismatch, got %v", err)
	}
	requests, _ := f.transport.counts()
	if requests != 0 {
		t.Fatalf("out-of-scope request reached transport %d times", requests)
	}
	encoded, err := json.Marshal(f.connection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-zammad-token") || strings.Contains(string(encoded), "credential_id") {
		t.Fatalf("connection API model disclosed credential material: %s", encoded)
	}
	var encrypted string
	if err := f.db.QueryRow("SELECT encrypted_secret FROM action_credentials WHERE id = ?", f.connection.CredentialID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == "synthetic-zammad-token" || strings.Contains(encrypted, "synthetic-zammad-token") {
		t.Fatal("Zammad token was stored in plaintext")
	}
	listed, err := f.credentials.ListForWorkspace(f.workspace1)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("managed Zammad credential leaked into generic list: %#v", listed)
	}
	if _, err := f.credentials.Get(f.connection.CredentialID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("managed Zammad credential was addressable through generic CRUD: %v", err)
	}
	if _, _, err := f.credentials.Resolve(context.Background(), f.connection.CredentialID, f.workspace1); !errors.Is(err, ErrCredentialPurposeMismatch) {
		t.Fatalf("managed Zammad credential resolved through generic action path: %v", err)
	}
}

func TestZammadDeleteConnectionRejectsExistingTicketLinks(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteConnection(f.connection.ProviderID); err == nil {
		t.Fatal("connection deletion succeeded despite existing ticket link")
	}
	var providerCount, credentialCount, linkCount int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM integration_providers WHERE id = ?", f.connection.ProviderID).Scan(&providerCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials WHERE id = ?", f.connection.CredentialID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_ticket_links WHERE id = ?", link.ID).Scan(&linkCount); err != nil {
		t.Fatal(err)
	}
	if providerCount != 1 || credentialCount != 1 || linkCount != 1 {
		t.Fatalf("rejected connection delete changed persisted data: providers=%d credentials=%d links=%d", providerCount, credentialCount, linkCount)
	}
}

func TestZammadDeleteConnectionWithoutTicketLinksRemovesOwnedCredentialAtomically(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	credentialID := f.connection.CredentialID
	if err := f.service.DeleteConnection(f.connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	var providerCount, credentialCount int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM integration_providers WHERE id = ?", f.connection.ProviderID).Scan(&providerCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials WHERE id = ?", credentialID).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if providerCount != 0 || credentialCount != 0 {
		t.Fatalf("connection delete left data behind: providers=%d credentials=%d", providerCount, credentialCount)
	}
}

func TestZammadConnectionUpdateRollsBackManagedCredentialOnProviderConflict(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	other, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "other-helpdesk", Name: "Other helpdesk", BaseURL: "https://other.example.test",
		APIToken: "other-token", DefaultGroupID: 7, DefaultGroupName: "Support",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	newToken := "must-be-rolled-back"
	newScope := []int{f.workspace2}
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{Slug: &other.Slug, APIToken: &newToken, WorkspaceIDs: &newScope}); !errors.Is(err, repository.ErrDuplicateEntry) {
		t.Fatalf("expected duplicate provider slug, got %v", err)
	}
	secret, _, err := f.credentials.ResolveManaged(context.Background(), f.connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), f.connection.ProviderID)
	if err != nil || secret != "synthetic-zammad-token" {
		t.Fatalf("managed credential was not restored: secret_matches=%v err=%v", secret == "synthetic-zammad-token", err)
	}
	if _, _, err := f.credentials.ResolveManaged(context.Background(), f.connection.CredentialID, f.workspace2, string(models.IntegrationProviderZammad), f.connection.ProviderID); !errors.Is(err, ErrCredentialScopeMismatch) {
		t.Fatalf("managed credential scope was not restored: %v", err)
	}
}

func TestZammadSyncUpdatesStatusAndDisabledConnectionsAreSkipped(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 7, "state_id": 3, "state": "pending"}
	link, err = f.service.SyncTicketLink(context.Background(), link.ID)
	if err != nil || link.LastStatusID != 3 || link.LastStatusName != "pending" {
		t.Fatalf("unexpected synced link: link=%#v err=%v", link, err)
	}
	disabled := false
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	f.transport.resetRequests()
	if err := f.service.SyncDue(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	requests, _ := f.transport.counts()
	if requests != 0 {
		t.Fatalf("disabled connection was polled %d times", requests)
	}
}

func TestZammadDisabledConnectionCanStillBeTestedByAdmin(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	disabled := false
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	metadata, err := f.service.TestConnection(context.Background(), f.connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Groups) != 1 || metadata.Groups[0].Name != "Support" {
		t.Fatalf("unexpected connection test metadata: %#v", metadata)
	}
}

func TestZammadUnknownTicketDoesNotChangeItem(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	f.transport.getStatus = http.StatusNotFound
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err == nil {
		t.Fatal("expected remote not-found error")
	}
	var statusID int
	if err := f.db.QueryRow("SELECT status_id FROM items WHERE id = ?", f.item1).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != f.openStatus {
		t.Fatalf("unknown remote ticket changed item status to %d", statusID)
	}
	stored, err := f.service.GetTicketLink(link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.LastError, "synthetic remote detail") {
		t.Fatalf("remote response body leaked into stored error: %q", stored.LastError)
	}
}

func TestZammadClosedStateCompletesItemOnce(t *testing.T) {
	workflow := &fakeZammadWorkflow{}
	f := newZammadServiceFixture(t, workflow)
	workflow.db = f.db
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	closed := []int{4}
	if _, err := f.service.UpdateConnection(f.connection.ProviderID, models.UpdateZammadConnectionRequest{ClosedStateIDs: &closed, CompletionStatusID: &f.doneStatus}); err != nil {
		t.Fatal(err)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 7, "state_id": 4, "state": "closed"}
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	var statusID int
	if err := f.db.QueryRow("SELECT status_id FROM items WHERE id = ?", f.item1).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != f.doneStatus || workflow.writes != 1 {
		t.Fatalf("closed-state mapping was not idempotent: status=%d writes=%d", statusID, workflow.writes)
	}
	if _, err := f.db.ExecWrite("UPDATE items SET status_id = ? WHERE id = ?", f.openStatus, f.item1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT status_id FROM items WHERE id = ?", f.item1).Scan(&statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != f.openStatus || workflow.writes != 1 {
		t.Fatalf("same remote closed episode re-completed a reopened item: status=%d writes=%d", statusID, workflow.writes)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 7, "state_id": 2, "state": "open"}
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	f.transport.getTicket = map[string]any{"id": 901, "number": "420901", "group_id": 7, "state_id": 4, "state": "closed"}
	if _, err := f.service.SyncTicketLink(context.Background(), link.ID); err != nil {
		t.Fatal(err)
	}
	if workflow.writes != 2 {
		t.Fatalf("new remote closed episode did not complete the item: writes=%d", workflow.writes)
	}
}

func TestNormalizeZammadBaseURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{input: "https://support.example.test/desk/", want: "https://support.example.test/desk"},
		{input: "http://support.example.test", err: true},
		{input: "https://user:secret@support.example.test", err: true},
		{input: "https://support.example.test?token=secret", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := NormalizeZammadBaseURL(tt.input)
			if (err != nil) != tt.err || got != tt.want {
				t.Fatalf("NormalizeZammadBaseURL(%q) = %q, %v", tt.input, got, err)
			}
		})
	}
}

func TestZammadDueSyncUsesAgeThreshold(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	link, err := f.service.CreateTicket(context.Background(), f.item1, f.actorID, models.CreateZammadTicketRequest{ConnectionID: f.connection.ProviderID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecWrite("UPDATE zammad_ticket_links SET last_synced_at = ? WHERE id = ?", time.Now().Add(-10*time.Minute), link.ID); err != nil {
		t.Fatal(err)
	}
	f.transport.resetRequests()
	if err := f.service.SyncDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	requests, _ := f.transport.counts()
	if requests != 1 {
		t.Fatalf("expected one ticket refresh, got %d transport requests", requests)
	}
}

func TestZammadOAuthConnectionStoresConnectionTokensAndConsumesStateOnce(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, method, targetURL string, body []byte, headers map[string]string) (*zammad.Response, error) {
		if method != http.MethodPost || targetURL != "https://oauth.example.test/oauth/token" || headers["Content-Type"] != "application/x-www-form-urlencoded" || strings.Contains(string(body), "synthetic-client-secret") == false {
			t.Fatalf("unexpected OAuth token request: %s %s %q %#v", method, targetURL, body, headers)
		}
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "oauth-access-token", "refresh_token": "oauth-refresh-token", "expires_in": 7200}), nil
	}))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "oauth-helpdesk", Name: "OAuth helpdesk", BaseURL: "https://oauth.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "synthetic-client", OAuthClientSecret: "synthetic-client-secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	if connection.CredentialID <= 0 || connection.OAuthConnected || connection.HasAPIToken {
		t.Fatalf("OAuth connection should have a non-token pending credential before callback: %#v", connection)
	}
	pending, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil || strings.Contains(pending, "token") || !strings.Contains(pending, "pending") {
		t.Fatalf("OAuth pending credential is unsafe: %q, %v", pending, err)
	}
	var credentialsBefore int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials").Scan(&credentialsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "oauth-helpdesk", Name: "Duplicate OAuth helpdesk", BaseURL: "https://duplicate.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret", DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID); !errors.Is(err, repository.ErrDuplicateEntry) {
		t.Fatalf("expected duplicate OAuth connection error, got %v", err)
	}
	var credentialsAfter int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM action_credentials").Scan(&credentialsAfter); err != nil || credentialsAfter != credentialsBefore {
		t.Fatalf("failed OAuth creation left a managed credential behind: before=%d after=%d err=%v", credentialsBefore, credentialsAfter, err)
	}
	authURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/oauth/authorize" || parsed.Query().Get("scope") != "full" || parsed.Query().Get("redirect_uri") != "https://windshift.example.test/api/integrations/zammad/oauth/callback" {
		t.Fatalf("unexpected authorization URL: %s", authURL)
	}
	if _, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "http://windshift.example.test"); err == nil {
		t.Fatal("OAuth start accepted a non-HTTPS public base URL")
	}
	abortedURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	abortedState, err := url.Parse(abortedURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.InvalidateOAuthState(abortedState.Query().Get("state")); err != nil {
		t.Fatal(err)
	}
	invalidated, err := f.service.GetConnection(connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if invalidated.OAuthAttemptID != "" {
		t.Fatalf("invalidated OAuth state retained connection attempt %q", invalidated.OAuthAttemptID)
	}
	if _, err := f.service.CompleteOAuth(context.Background(), abortedState.Query().Get("state"), "aborted-code", "https://windshift.example.test"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("aborted OAuth state remained usable: %v", err)
	}
	usableURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	usableState, err := url.Parse(usableURL)
	if err != nil {
		t.Fatal(err)
	}
	state := usableState.Query().Get("state")
	if _, err := f.service.CompleteOAuth(context.Background(), state, "authorization-code", "https://windshift.example.test"); err != nil {
		t.Fatal(err)
	}
	completed, err := f.service.GetConnection(connection.ProviderID)
	if err != nil || !completed.OAuthConnected || completed.HasAPIToken {
		t.Fatalf("unexpected OAuth connection status after callback: %#v, %v", completed, err)
	}
	if _, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test"); err != nil {
		t.Fatal(err)
	}
	newClientID := "rotated-client"
	reset, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{OAuthClientID: &newClientID})
	if err != nil || reset.OAuthConnected || reset.ReauthorizationRequired || reset.HasAPIToken {
		t.Fatalf("OAuth credential change did not reset authorization: %#v, %v", reset, err)
	}
	var stateCount, tokenCount int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_oauth_state WHERE provider_id = ?", connection.ProviderID).Scan(&stateCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_oauth_tokens WHERE provider_id = ?", connection.ProviderID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if stateCount != 0 || tokenCount != 0 {
		t.Fatalf("OAuth reset retained state=%d token=%d", stateCount, tokenCount)
	}
	bundle, err := activeZammadOAuthCredential("access", "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.credentials.RotateManaged(connection.CredentialID, models.RotateActionCredentialRequest{Secret: bundle}, string(models.IntegrationProviderZammad), connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewZammadRepository(f.db).UpsertOAuthToken(repository.ZammadOAuthToken{ProviderID: connection.ProviderID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test"); err != nil {
		t.Fatal(err)
	}
	newBaseURL := "https://other-zammad.example.test"
	reset, err = f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{BaseURL: &newBaseURL})
	if err != nil || reset.OAuthConnected || reset.ReauthorizationRequired {
		t.Fatalf("OAuth base URL change did not reset authorization: %#v, %v", reset, err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_oauth_state WHERE provider_id = ?", connection.ProviderID).Scan(&stateCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_oauth_tokens WHERE provider_id = ?", connection.ProviderID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if stateCount != 0 || tokenCount != 0 {
		t.Fatalf("OAuth base URL reset retained state=%d token=%d", stateCount, tokenCount)
	}
	if _, err := f.service.CompleteOAuth(context.Background(), state, "authorization-code", "https://windshift.example.test"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("OAuth state was not consumed exactly once: %v", err)
	}
	var encryptedBundle string
	if err := f.db.QueryRow("SELECT encrypted_secret FROM action_credentials WHERE id = ?", connection.CredentialID).Scan(&encryptedBundle); err != nil {
		t.Fatal(err)
	}
	if encryptedBundle == "oauth-access-token" || strings.Contains(encryptedBundle, "oauth-access-token") || strings.Contains(encryptedBundle, "oauth-refresh-token") {
		t.Fatal("OAuth tokens were not encrypted at rest")
	}
}

func createActiveZammadOAuthConnection(t *testing.T, f *zammadServiceFixture, slug string, expiresAt time.Time) *models.ZammadConnection {
	t.Helper()
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: slug, Name: "OAuth " + slug, BaseURL: "https://" + slug + ".example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := activeZammadOAuthCredential("old-access", "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.credentials.RotateManaged(connection.CredentialID, models.RotateActionCredentialRequest{Secret: bundle}, string(models.IntegrationProviderZammad), connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewZammadRepository(f.db).UpsertOAuthToken(repository.ZammadOAuthToken{
		ProviderID: connection.ProviderID, OAuthGeneration: connection.OAuthGeneration, ExpiresAt: expiresAt,
	}); err != nil {
		t.Fatal(err)
	}
	connection, err = f.service.GetConnection(connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func assertZammadManagedCredentialMetadata(t *testing.T, f *zammadServiceFixture, connection *models.ZammadConnection, wantName string, wantWorkspaceID int) {
	t.Helper()
	credential, err := repository.NewActionCredentialRepository(f.db).GetActionCredentialByID(connection.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Name != wantName || credential.AppliesToAllWorkspaces || len(credential.WorkspaceIDs) != 1 || credential.WorkspaceIDs[0] != wantWorkspaceID {
		t.Fatalf("OAuth secret write overwrote concurrent credential metadata: %#v", credential)
	}
}

func TestZammadOAuthCallbackCannotCommitAfterConfigurationReset(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "callback-race", Name: "OAuth callback race", BaseURL: "https://callback-race.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "stale-access", "refresh_token": "stale-refresh", "expires_in": 3600}), nil
	}))
	type callbackResult struct {
		err error
	}
	done := make(chan callbackResult, 1)
	go func() {
		_, err := f.service.CompleteOAuth(context.Background(), parsed.Query().Get("state"), "code", "https://windshift.example.test")
		done <- callbackResult{err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth callback did not reach token exchange")
	}
	newClientID := "replacement-client"
	reset, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{OAuthClientID: &newClientID})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if !errors.Is(result.err, ErrZammadOAuthSuperseded) {
		t.Fatalf("stale callback commit error = %v", result.err)
	}
	if reset.OAuthGeneration <= connection.OAuthGeneration || reset.OAuthConnected {
		t.Fatalf("reset did not advance generation and clear authorization: %#v", reset)
	}
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil || !strings.Contains(raw, `"status":"pending"`) || strings.Contains(raw, "stale-access") {
		t.Fatalf("stale callback reactivated credential: raw=%q err=%v", raw, err)
	}
}

func TestZammadOAuthCallbackPreservesConcurrentNameAndScopeUpdate(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "callback-metadata-race", Name: "Original callback name", BaseURL: "https://callback-metadata-race.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	authURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "callback-access", "refresh_token": "callback-refresh", "expires_in": 3600}), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.service.CompleteOAuth(context.Background(), parsed.Query().Get("state"), "code", "https://windshift.example.test")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth callback did not reach token exchange")
	}
	updatedName := "Renamed during callback"
	updatedScope := []int{f.workspace2}
	if _, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{Name: &updatedName, WorkspaceIDs: &updatedScope}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertZammadManagedCredentialMetadata(t, f, connection, updatedName+" Zammad OAuth credentials", f.workspace2)
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace2, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parseZammadOAuthCredential(raw)
	if err != nil || bundle.AccessToken != "callback-access" {
		t.Fatalf("callback secret was not committed: bundle=%#v err=%v", bundle, err)
	}
}

func TestZammadOAuthParallelStartsLeaveOnlyNewestUsableState(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "parallel-state", Name: "Parallel OAuth state", BaseURL: "https://parallel-state.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "parallel-access", "refresh_token": "parallel-refresh", "expires_in": 3600}), nil
	}))
	type startResult struct {
		state string
		err   error
	}
	results := make(chan startResult, 2)
	start := func() {
		authURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
		if err != nil {
			results <- startResult{err: err}
			return
		}
		parsed, parseErr := url.Parse(authURL)
		if parseErr != nil {
			results <- startResult{err: parseErr}
			return
		}
		results <- startResult{state: parsed.Query().Get("state")}
	}
	go start()
	go start()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.state == "" || second.state == "" || first.state == second.state {
		t.Fatalf("parallel OAuth starts failed: first=%#v second=%#v", first, second)
	}
	var persistedState string
	var stateCount int
	if err := f.db.QueryRow("SELECT state FROM zammad_oauth_state WHERE provider_id = ?", connection.ProviderID).Scan(&persistedState); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM zammad_oauth_state WHERE provider_id = ?", connection.ProviderID).Scan(&stateCount); err != nil || stateCount != 1 {
		t.Fatalf("parallel starts retained %d states: %v", stateCount, err)
	}
	staleState := first.state
	if persistedState == first.state {
		staleState = second.state
	}
	if _, err := f.service.CompleteOAuth(context.Background(), staleState, "stale-code", "https://windshift.example.test"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("superseded parallel state remained usable: %v", err)
	}
	if _, err := f.service.CompleteOAuth(context.Background(), persistedState, "winning-code", "https://windshift.example.test"); err != nil {
		t.Fatalf("winning parallel state failed: %v", err)
	}
}

func TestZammadOAuthNewStartSupersedesAlreadyRunningCallback(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "running-callback", Name: "Running OAuth callback", BaseURL: "https://running-callback.example.test",
		AuthMethod: models.ZammadAuthMethodOAuth, OAuthClientID: "client", OAuthClientSecret: "secret",
		DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	oldURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	oldParsed, err := url.Parse(oldURL)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var transportMu sync.Mutex
	transportCalls := 0
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		transportMu.Lock()
		transportCalls++
		call := transportCalls
		transportMu.Unlock()
		if call == 1 {
			close(entered)
			<-release
			return jsonResponse(http.StatusOK, map[string]any{"access_token": "superseded-access", "refresh_token": "superseded-refresh", "expires_in": 3600}), nil
		}
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "winning-access", "refresh_token": "winning-refresh", "expires_in": 3600}), nil
	}))
	oldDone := make(chan error, 1)
	go func() {
		_, err := f.service.CompleteOAuth(context.Background(), oldParsed.Query().Get("state"), "old-code", "https://windshift.example.test")
		oldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old callback did not reach token exchange")
	}
	newURL, err := f.service.StartOAuth(context.Background(), connection.ProviderID, f.actorID, "https://windshift.example.test")
	if err != nil {
		t.Fatal(err)
	}
	newParsed, err := url.Parse(newURL)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-oldDone; !errors.Is(err, ErrZammadOAuthSuperseded) {
		t.Fatalf("already-running old callback commit error = %v", err)
	}
	if _, err := f.service.CompleteOAuth(context.Background(), newParsed.Query().Get("state"), "new-code", "https://windshift.example.test"); err != nil {
		t.Fatalf("new callback failed after superseding running callback: %v", err)
	}
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parseZammadOAuthCredential(raw)
	if err != nil || bundle.AccessToken != "winning-access" || bundle.RefreshToken != "winning-refresh" {
		t.Fatalf("superseded callback overwrote winning tokens: bundle=%#v err=%v", bundle, err)
	}
}

func TestZammadOAuthRefreshCannotCommitAfterConfigurationReset(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "refresh-race", time.Now().Add(-time.Minute))
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "stale-refreshed-access", "refresh_token": "stale-refreshed-refresh", "expires_in": 3600}), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth refresh did not reach token exchange")
	}
	newClientID := "replacement-client"
	reset, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{OAuthClientID: &newClientID})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrZammadOAuthSuperseded) {
		t.Fatalf("stale refresh commit error = %v", err)
	}
	if reset.OAuthGeneration <= connection.OAuthGeneration || reset.OAuthConnected {
		t.Fatalf("reset did not advance generation and clear authorization: %#v", reset)
	}
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil || !strings.Contains(raw, `"status":"pending"`) || strings.Contains(raw, "stale-refreshed-access") {
		t.Fatalf("stale refresh reactivated credential: raw=%q err=%v", raw, err)
	}
}

func TestZammadOAuthRefreshPreservesConcurrentNameAndScopeUpdate(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "refresh-metadata-race", time.Now().Add(-time.Minute))
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "refreshed-access", "refresh_token": "refreshed-refresh", "expires_in": 3600}), nil
	}))
	done := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
		done <- struct {
			token string
			err   error
		}{token: token, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth refresh did not reach token exchange")
	}
	updatedName := "Renamed during refresh"
	updatedScope := []int{f.workspace2}
	if _, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{Name: &updatedName, WorkspaceIDs: &updatedScope}); err != nil {
		t.Fatal(err)
	}
	close(release)
	result := <-done
	if result.err != nil || result.token != "refreshed-access" {
		t.Fatalf("refresh result: token=%q err=%v", result.token, result.err)
	}
	assertZammadManagedCredentialMetadata(t, f, connection, updatedName+" Zammad OAuth credentials", f.workspace2)
}

func TestZammadOAuthRefreshCommitRequiresCurrentClaimOwner(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "claim-loss", time.Now().Add(-time.Minute))
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "claim-lost-access", "refresh_token": "claim-lost-refresh", "expires_in": 3600}), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth refresh did not reach token exchange")
	}
	var claimOwner string
	var leaseUntil time.Time
	if err := f.db.QueryRow("SELECT refresh_claim_owner, refresh_lock_until FROM zammad_oauth_tokens WHERE provider_id = ?", connection.ProviderID).Scan(&claimOwner, &leaseUntil); err != nil {
		t.Fatal(err)
	}
	if claimOwner == "" || time.Until(leaseUntil) <= zammadHTTPTimeout {
		t.Fatalf("refresh lease is not safely longer than HTTP timeout: owner=%q remaining=%s", claimOwner, time.Until(leaseUntil))
	}
	if _, err := f.db.ExecWrite("UPDATE zammad_oauth_tokens SET refresh_claim_owner = ? WHERE provider_id = ?", "replacement-owner", connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrZammadOAuthSuperseded) {
		t.Fatalf("claim-lost refresh commit error = %v", err)
	}
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := parseZammadOAuthCredential(raw)
	if err != nil || bundle.AccessToken != "old-access" || bundle.RefreshToken != "old-refresh" {
		t.Fatalf("claim-lost refresh overwrote credential: bundle=%#v err=%v", bundle, err)
	}
}

func TestZammadOAuthRefreshRereadsRotatedCredentialAfterClaim(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "refresh-reread", time.Now().Add(-time.Minute))
	beforeClaim, continueClaim := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthBeforeRefreshClaimForTesting(func() {
		close(beforeClaim)
		<-continueClaim
	})
	var transportMu sync.Mutex
	transportCalls := 0
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		transportMu.Lock()
		transportCalls++
		transportMu.Unlock()
		return jsonResponse(http.StatusOK, map[string]any{"access_token": "unexpected-access", "refresh_token": "unexpected-refresh", "expires_in": 3600}), nil
	}))
	done := make(chan struct {
		token string
		err   error
	}, 1)
	go func() {
		token, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
		done <- struct {
			token string
			err   error
		}{token: token, err: err}
	}()
	select {
	case <-beforeClaim:
	case <-time.After(5 * time.Second):
		t.Fatal("second refresh request did not pause before its claim")
	}
	rotatedBundle, err := activeZammadOAuthCredential("already-refreshed-access", "already-rotated-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.credentials.RotateManaged(connection.CredentialID, models.RotateActionCredentialRequest{Secret: rotatedBundle}, string(models.IntegrationProviderZammad), connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewZammadRepository(f.db).UpsertOAuthToken(repository.ZammadOAuthToken{
		ProviderID: connection.ProviderID, OAuthGeneration: connection.OAuthGeneration, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	close(continueClaim)
	result := <-done
	if result.err != nil || result.token != "already-refreshed-access" {
		t.Fatalf("second request did not reuse freshly rotated credential: token=%q err=%v", result.token, result.err)
	}
	transportMu.Lock()
	calls := transportCalls
	transportMu.Unlock()
	if calls != 0 {
		t.Fatalf("second request reused a stale refresh token in %d upstream calls", calls)
	}
	var owner *string
	if err := f.db.QueryRow("SELECT refresh_claim_owner FROM zammad_oauth_tokens WHERE provider_id = ?", connection.ProviderID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Fatalf("fresh-token fast path retained refresh claim %q", *owner)
	}
}

func TestZammadOAuthInvalidGrantReturnsPersistenceFailure(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "invalid-grant-persist", time.Now().Add(-time.Minute))
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid_grant"}), nil
	}))
	if _, err := f.db.ExecWrite(`CREATE TRIGGER fail_zammad_reauthorization
		BEFORE UPDATE ON action_credentials
		BEGIN SELECT RAISE(ABORT, 'synthetic credential persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
	if err == nil || errors.Is(err, ErrZammadReauthorizationRequired) {
		t.Fatalf("invalid_grant persistence failure was hidden: %v", err)
	}
	token, tokenErr := repository.NewZammadRepository(f.db).GetOAuthToken(connection.ProviderID)
	if tokenErr != nil || token.ReauthorizationRequired {
		t.Fatalf("failed transaction partially marked reauthorization: token=%#v err=%v", token, tokenErr)
	}
	raw, _, resolveErr := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace1, string(models.IntegrationProviderZammad), connection.ProviderID)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	bundle, parseErr := parseZammadOAuthCredential(raw)
	if parseErr != nil || bundle.AccessToken != "old-access" {
		t.Fatalf("failed transaction partially replaced credential: bundle=%#v err=%v", bundle, parseErr)
	}
}

func TestZammadOAuthInvalidGrantPreservesConcurrentNameAndScopeUpdate(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	connection := createActiveZammadOAuthConnection(t, f, "invalid-grant-metadata-race", time.Now().Add(-time.Minute))
	entered, release := make(chan struct{}), make(chan struct{})
	f.service.SetOAuthTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		close(entered)
		<-release
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid_grant"}), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := f.service.oauthAccessToken(context.Background(), connection, f.workspace1)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("OAuth invalid_grant test did not reach token exchange")
	}
	updatedName := "Renamed during invalid grant"
	updatedScope := []int{f.workspace2}
	if _, err := f.service.UpdateConnection(connection.ProviderID, models.UpdateZammadConnectionRequest{Name: &updatedName, WorkspaceIDs: &updatedScope}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrZammadReauthorizationRequired) {
		t.Fatalf("invalid_grant result = %v", err)
	}
	assertZammadManagedCredentialMetadata(t, f, connection, updatedName+" Zammad OAuth credentials", f.workspace2)
	raw, _, err := f.credentials.ResolveManaged(context.Background(), connection.CredentialID, f.workspace2, string(models.IntegrationProviderZammad), connection.ProviderID)
	if err != nil || !strings.Contains(raw, `"status":"reauthorization_required"`) {
		t.Fatalf("invalid_grant did not persist reauthorization secret: raw=%q err=%v", raw, err)
	}
}

func TestZammadOAuthTestSkipsAdminOnlyCorrelationFieldCheck(t *testing.T) {
	f := newZammadServiceFixture(t, nil)
	f.service.SetOAuthEncryption(sso.NewSecretEncryption("synthetic-server-secret-for-zammad-tests"))
	connection, err := f.service.CreateConnection(models.CreateZammadConnectionRequest{
		Slug: "oauth-scope", Name: "Scoped OAuth", BaseURL: "https://scope.example.test", AuthMethod: models.ZammadAuthMethodOAuth,
		OAuthClientID: "client", OAuthClientSecret: "secret", DefaultCustomer: "robot@example.test", WorkspaceIDs: []int{f.workspace1},
	}, f.actorID)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := activeZammadOAuthCredential("access", "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.credentials.RotateManaged(connection.CredentialID, models.RotateActionCredentialRequest{Secret: bundle}, string(models.IntegrationProviderZammad), connection.ProviderID); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewZammadRepository(f.db).UpsertOAuthToken(repository.ZammadOAuthToken{ProviderID: connection.ProviderID, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	calledObjectManager := false
	f.service.SetTransportForTesting(zammad.TransportFunc(func(_ context.Context, _ string, targetURL string, _ []byte, _ map[string]string) (*zammad.Response, error) {
		if strings.Contains(targetURL, "object_manager_attributes") {
			calledObjectManager = true
			return jsonResponse(http.StatusForbidden, map[string]string{}), nil
		}
		if strings.Contains(targetURL, "/groups") {
			return jsonResponse(http.StatusOK, []map[string]any{{"id": 7, "name": "Support", "active": true}}), nil
		}
		return jsonResponse(http.StatusOK, []map[string]any{{"id": 2, "name": "open", "active": true}}), nil
	}))
	metadata, err := f.service.TestConnection(context.Background(), connection.ProviderID)
	if err != nil || metadata.CorrelationFieldVerified || calledObjectManager {
		t.Fatalf("OAuth test must not require admin.object: metadata=%#v err=%v called=%v", metadata, err, calledObjectManager)
	}
}

func TestZammadSafeTransportHonorsAllowLocalConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/groups" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	previous := utils.AllowLocalConnections()
	defer utils.SetAllowLocalConnections(previous)
	transport := newZammadSafeTransport(server.URL, "/api/v1/")
	utils.SetAllowLocalConnections(true)
	if _, err := transport.Do(context.Background(), http.MethodGet, server.URL+"/api/v1/groups", nil, nil); err != nil {
		t.Fatalf("ALLOW_LOCAL_CONNECTIONS=true blocked local Zammad target: %v", err)
	}
	utils.SetAllowLocalConnections(false)
	if _, err := transport.Do(context.Background(), http.MethodGet, server.URL+"/api/v1/groups", nil, nil); !errors.Is(err, utils.ErrBlockedSSRFAddr) {
		t.Fatalf("ALLOW_LOCAL_CONNECTIONS=false did not block local Zammad target: %v", err)
	}
}

func TestZammadClientsUseExactlyOneExpectedAuthorizationScheme(t *testing.T) {
	for _, testCase := range []struct {
		name, want string
		client     *zammad.Client
	}{
		{name: "legacy", want: "Token token=legacy-token", client: zammad.NewClient("https://zammad.example.test", "legacy-token", zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, headers map[string]string) (*zammad.Response, error) {
			if headers["Authorization"] != "Token token=legacy-token" {
				t.Fatalf("legacy authorization = %q", headers["Authorization"])
			}
			return jsonResponse(http.StatusOK, []map[string]any{}), nil
		}))},
		{name: "oauth", want: "Bearer oauth-token", client: zammad.NewOAuthClient("https://zammad.example.test", "oauth-token", zammad.TransportFunc(func(_ context.Context, _ string, _ string, _ []byte, headers map[string]string) (*zammad.Response, error) {
			if headers["Authorization"] != "Bearer oauth-token" {
				t.Fatalf("OAuth authorization = %q", headers["Authorization"])
			}
			return jsonResponse(http.StatusOK, []map[string]any{}), nil
		}))},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.client.Groups(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
