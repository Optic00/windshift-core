package netbox

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"windshift/internal/models"
	"windshift/internal/utils"
)

func TestSafeTransportRejectsMethodOriginAndPathsBeforeNetwork(t *testing.T) {
	t.Parallel()
	transport := NewSafeTransport("https://netbox.example/root")
	for name, request := range map[string][2]string{
		"method":        {http.MethodPost, "https://netbox.example/root/api/dcim/devices/"},
		"origin":        {http.MethodGet, "https://attacker.invalid/root/api/dcim/devices/"},
		"admin":         {http.MethodGet, "https://netbox.example/root/api/users/tokens/"},
		"missing slash": {http.MethodGet, "https://netbox.example/root/api/dcim/devices"},
		"non numeric":   {http.MethodGet, "https://netbox.example/root/api/dcim/devices/latest/"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := transport.Do(context.Background(), request[0], request[1], nil, nil); err == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
}

func TestSafeTransportRealTLSGetRedirectAndResponseLimit(t *testing.T) {
	// These globals affect only this disposable test process and are restored.
	previousLocal, previousTLS := utils.AllowLocalConnections(), utils.SkipTLSVerify()
	utils.SetAllowLocalConnections(true)
	utils.SetSkipTLSVerify(false)
	t.Cleanup(func() {
		utils.SetAllowLocalConnections(previousLocal)
		utils.SetSkipTLSVerify(previousTLS)
	})
	var foreignRequests atomic.Int32
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer foreign.Close()
	var mode atomic.Int32
	var authorizedRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/dcim/devices/" || r.Header.Get("Authorization") != "Bearer nbt_key.synthetic" {
			t.Error("unexpected authenticated request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		authorizedRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch mode.Load() {
		case 1:
			w.Header().Set("Location", foreign.URL+"/api/dcim/devices/")
			w.WriteHeader(http.StatusFound)
		case 2:
			_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBody+1)))
		default:
			_, _ = w.Write([]byte(`{"count":1,"results":[{"id":42,"name":"synthetic-router"}]}`))
		}
	}))
	defer server.Close()
	transport := NewSafeTransport(server.URL).(*safeTransport)
	httpTransport := transport.client.Transport.(*http.Transport)
	if httpTransport.Proxy != nil || !httpTransport.DisableKeepAlives || httpTransport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("transport inherited a proxy, idle connection pool or disabled certificate verification")
	}
	// Trust only this synthetic server's certificate, without weakening TLS.
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	httpTransport.TLSClientConfig.RootCAs = pool
	client, err := NewClient(server.URL, "nbt_key.synthetic", models.NetBoxAuthBearer, transport)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Search(context.Background(), models.NetBoxDevice, "router", 20, 0)
	if err != nil || len(result.Results) != 1 || result.Results[0].ObjectID != 42 {
		t.Fatalf("real TLS GET failed: %v", err)
	}
	mode.Store(1)
	_, err = client.Search(context.Background(), models.NetBoxDevice, "router", 20, 0)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound || foreignRequests.Load() != 0 {
		t.Fatal("redirect was not rejected before sending the token elsewhere")
	}
	mode.Store(2)
	_, err = client.Search(context.Background(), models.NetBoxDevice, "router", 20, 0)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || authorizedRequests.Load() != 3 {
		t.Fatal("oversized response was not rejected")
	}
	utils.SetAllowLocalConnections(false)
	_, err = client.Search(context.Background(), models.NetBoxDevice, "router", 20, 0)
	if !errors.Is(err, utils.ErrBlockedSSRFAddr) || authorizedRequests.Load() != 3 {
		t.Fatal("explicit private-address blocking did not prevent the connection")
	}
}
