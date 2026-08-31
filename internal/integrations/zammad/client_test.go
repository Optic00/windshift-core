package zammad

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type httpTestTransport struct{ client *http.Client }

func (t httpTestTransport) Do(ctx context.Context, method, targetURL string, body []byte, headers map[string]string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &Response{StatusCode: resp.StatusCode, Body: responseBody}, nil
}

func TestClientMetadataCorrelationAndTicketCreation(t *testing.T) {
	t.Parallel()
	const token = "synthetic-secret-token"
	postCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token token="+token {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`[{"id":1,"name":"Support","active":true},{"id":2,"name":"Old","active":false}]`))
		case r.URL.Path == "/api/v1/ticket_states":
			_, _ = w.Write([]byte(`[{"id":4,"name":"closed","state_type_id":5,"active":true}]`))
		case r.URL.Path == "/api/v1/object_manager_attributes":
			_, _ = w.Write([]byte(`[{"name":"windshift_item_key","object":"Ticket","active":true}]`))
		case r.URL.Path == "/api/v1/tickets/search":
			_, _ = w.Write([]byte(`[{"id":9,"number":"42009","group_id":1,"state_id":2,"state":"open","windshift_item_key":"windshift:abc:ITEM-49"}]`))
		case r.URL.Path == "/api/v1/tickets" && r.Method == http.MethodPost:
			postCount++
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"windshift_item_key":"windshift:abc:ITEM-50"`) {
				t.Fatalf("ticket payload lacks correlation field: %s", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":10,"number":"42010","group_id":1,"state_id":2,"state":"open"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, token, httpTestTransport{client: server.Client()})
	metadata, err := client.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Groups) != 1 || metadata.Groups[0].Name != "Support" || len(metadata.States) != 1 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if err := client.ValidateCorrelationField(context.Background(), "windshift_item_key"); err != nil {
		t.Fatal(err)
	}
	found, err := client.FindByCorrelation(context.Background(), "windshift_item_key", "windshift:abc:ITEM-49")
	if err != nil || found == nil || found.ID != 9 {
		t.Fatalf("unexpected search result: ticket=%#v err=%v", found, err)
	}
	created, err := client.CreateTicket(context.Background(), "ITEM-50", "Synthetic body", "robot@example.test", "Support", "windshift_item_key", "windshift:abc:ITEM-50")
	if err != nil || created.ID != 10 || postCount != 1 {
		t.Fatalf("unexpected create result: ticket=%#v posts=%d err=%v", created, postCount, err)
	}
}

func TestClientRedactsAPIErrorBodyAndToken(t *testing.T) {
	t.Parallel()
	const token = "must-not-appear"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token must-not-appear is invalid"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, token, httpTestTransport{client: server.Client()})
	_, err := client.Metadata(context.Background())
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "invalid") {
		t.Fatalf("API error disclosed response content: %v", err)
	}
}

func TestClientHonorsContextTimeout(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	client := NewClient(server.URL, "token", httpTestTransport{client: server.Client()})
	_, err := client.Metadata(ctx)
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestClientFindByNumberRequiresExactMatch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tickets/search" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("query"); got != `number:"42009"` {
			t.Fatalf("unexpected ticket-number query: %q", got)
		}
		_, _ = w.Write([]byte(`[{"id":9,"number":"420090"},{"id":10,"number":"42008"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", httpTestTransport{client: server.Client()})
	found, err := client.FindByNumber(context.Background(), "42009")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Fatalf("non-exact search result was accepted: %#v", found)
	}
}

func TestClientOwnersUsesGroupPermissionQueryAndFiltersIneligibleUsers(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/search" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if query.Get("query") != "*" || query.Get("permissions[]") != "ticket.agent" || query.Get("group_ids[7]") != "full" || query.Get("per_page") != "100" {
			t.Fatalf("unexpected owner query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[
			{"id":2,"active":true,"firstname":"Ada","lastname":"Lovelace","group_ids":{"7":["full"]}},
			{"id":6,"active":true,"login":"change-only","group_ids":{"7":["change"]}},
			{"id":3,"active":true,"login":"read-only","group_ids":{"7":["read"]}},
			{"id":4,"active":false,"login":"inactive","group_ids":{"7":["full"]}},
			{"id":5,"active":true,"login":"fallback","group_ids":{"8":["full"]}}
		]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "token", httpTestTransport{client: server.Client()})
	owners, err := client.Owners(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0] != (Owner{ID: 2, Name: "Ada Lovelace"}) {
		t.Fatalf("unexpected eligible owners: %#v", owners)
	}
}
