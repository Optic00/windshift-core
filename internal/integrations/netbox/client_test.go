package netbox

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"windshift/internal/models"
)

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	got, err := NormalizeBaseURL("https://netbox.example/prefix/")
	if err != nil || got != "https://netbox.example/prefix" {
		t.Fatalf("NormalizeBaseURL() = %q, %v", got, err)
	}
	bad := []string{
		"http://netbox.example", "https://user@netbox.example", "https://netbox.example/?q=x",
		"https://netbox.example/#x", "https://netbox.example/a/../b", "https://netbox.example/a%2fb",
		"https://netbox.example/%00", " https://netbox.example",
		"https://netbox.example?", "https://netbox.example#", "https://:443",
		"https://netbox.example:", "https://netbox.example:65536", "https://netbox.example:0",
		"https://netbox.example/%252e%252e", "https://netbox.example/" + strings.Repeat("a", 2048),
	}
	for _, input := range bad {
		if _, err := NormalizeBaseURL(input); err == nil {
			t.Errorf("NormalizeBaseURL(%q) unexpectedly succeeded", input)
		} else {
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Errorf("error type = %T, want ValidationError", err)
			}
		}
	}
}

func TestSearchRejectsMissingEnvelopeAndUnsafeNumericIdentity(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `null`, `{"count":0}`, `{"results":[]}`, `{"count":null,"results":[]}`, `{"count":0,"results":null}`, `{"count":1,"results":[{"id":9007199254740992,"name":"rounded"}]}`} {
		t.Run(body, func(t *testing.T) {
			client := mustClient(t, TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
				return jsonResponse(200, body), nil
			}))
			if _, err := client.Search(context.Background(), models.NetBoxDevice, "router", 20, 0); err == nil {
				t.Fatal("invalid pagination envelope or imprecise browser identity accepted")
			}
		})
	}
}

func TestValidateTokenAndAuthorizationHeaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scheme models.NetBoxAuthScheme
		token  string
		want   string
	}{{models.NetBoxAuthBearer, "nbt_key.token", "Bearer nbt_key.token"}, {models.NetBoxAuthToken, "0123abcdef", "Token 0123abcdef"}}
	for _, tc := range tests {
		var authorization string
		transport := TransportFunc(func(_ context.Context, method, target string, body []byte, headers map[string]string) (*Response, error) {
			authorization = headers["Authorization"]
			if method != http.MethodGet || len(body) != 0 {
				t.Fatalf("unexpected request: %s, body length %d", method, len(body))
			}
			return jsonResponse(200, `{"count":0,"results":[]}`), nil
		})
		client, err := NewClient("https://netbox.example", tc.token, tc.scheme, transport)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Search(context.Background(), models.NetBoxDevice, "ab", 1, 0); err != nil {
			t.Fatal(err)
		}
		if authorization != tc.want {
			t.Errorf("Authorization = %q, want %q", authorization, tc.want)
		}
	}
	for _, tc := range []struct {
		token  string
		scheme models.NetBoxAuthScheme
	}{{"nbt_no-dot", models.NetBoxAuthBearer}, {"nbt_key.token\n", models.NetBoxAuthBearer}, {"not-hex", models.NetBoxAuthToken}, {strings.Repeat("a", 4097), models.NetBoxAuthToken}} {
		if err := ValidateToken(tc.token, tc.scheme); err == nil || strings.Contains(err.Error(), tc.token) {
			t.Errorf("unsafe validation result for token length %d: %v", len(tc.token), err)
		}
	}
}

func TestSearchBoundsProjectionAndAttackerNextIgnored(t *testing.T) {
	t.Parallel()
	var requested *url.URL
	transport := TransportFunc(func(_ context.Context, _, target string, _ []byte, _ map[string]string) (*Response, error) {
		requested, _ = url.Parse(target)
		return jsonResponse(200, `{"count":3,"next":"https://attacker.invalid/steal","results":[`+
			`{"id":7,"name":"switch","display":"ignored","status":{"value":"active","label":"Active"},`+
			`"site":{"name":"HQ"},"role":{"display":"Core"},"primary_ip4":{"address":"192.0.2.1/24"},`+
			`"primary_ip6":null,"secret":"drop me"}]}`), nil
	})
	client := mustClient(t, transport)
	result, err := client.Search(context.Background(), models.NetBoxDevice, "sw", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if requested.Path != "/root/api/dcim/devices/" || requested.Query().Get("fields") != fields || requested.Query().Get("ordering") != "id" || requested.Query().Get("q") != "sw" {
		t.Fatalf("unexpected request URL: %s", requested)
	}
	if !result.HasMore || result.NextOffset == nil || *result.NextOffset != 1 || !result.UnrequestedFields {
		t.Fatalf("unexpected page metadata: %+v", result)
	}
	object := result.Results[0]
	if object.ExternalID != "dcim.device:7" || object.URL != "https://netbox.example/root/dcim/devices/7/" || object.Status != "Active" || object.Site != "HQ" || object.Role != "Core" || object.PrimaryIPv4 != "192.0.2.1/24" {
		t.Fatalf("unexpected object: %+v", object)
	}
}

func TestGetObjectTypeAndIDChecks(t *testing.T) {
	t.Parallel()
	transport := TransportFunc(func(_ context.Context, _, target string, _ []byte, _ map[string]string) (*Response, error) {
		if strings.Contains(target, "virtual-machines") {
			return jsonResponse(200, `{"id":42,"name":"vm"}`), nil
		}
		return jsonResponse(200, `{"id":43,"name":"device"}`), nil
	})
	client := mustClient(t, transport)
	vm, err := client.GetObject(context.Background(), models.NetBoxVirtualMachine, 42)
	if err != nil || vm.ExternalID != "virtualization.virtualmachine:42" {
		t.Fatalf("VM = %+v, %v", vm, err)
	}
	if _, err := client.GetObject(context.Background(), models.NetBoxDevice, 42); err == nil {
		t.Fatal("mismatched detail ID accepted")
	} else {
		var upstream *UpstreamError
		if !errors.As(err, &upstream) {
			t.Fatalf("error type = %T, want UpstreamError", err)
		}
	}
	if _, err := client.GetObject(context.Background(), models.NetBoxObjectType("x"), 1); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestMalformedOversizedAndArrayBounds(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]*Response{
		"malformed": {StatusCode: 200, Body: []byte(`{"count":1`)},
		"oversized": {StatusCode: 200, Body: []byte(strings.Repeat("x", maxResponseBody+1))},
		"array":     jsonResponse(200, `{"count":2,"results":[{"id":1},{"id":2}]}`),
		"string":    jsonResponse(200, `{"count":1,"results":[{"id":1,"name":"`+strings.Repeat("x", 257)+`"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			client := mustClient(t, TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
				return response, nil
			}))
			if _, err := client.Search(context.Background(), models.NetBoxDevice, "ab", 1, 0); err == nil {
				t.Fatal("invalid response accepted")
			} else {
				var upstream *UpstreamError
				if !errors.As(err, &upstream) {
					t.Fatalf("error type = %T", err)
				}
			}
		})
	}
}

func TestTestUsesBothCollectionsWithoutQueryAndWarns(t *testing.T) {
	t.Parallel()
	var targets []string
	transport := TransportFunc(func(_ context.Context, _, target string, _ []byte, _ map[string]string) (*Response, error) {
		targets = append(targets, target)
		return jsonResponse(200, `{"count":1,"results":[{"id":1,"unexpected":"ignored"}]}`), nil
	})
	client := mustClient(t, transport)
	result, err := client.Test(context.Background())
	if err != nil || !result.OK || len(result.Warnings) != 2 || len(targets) != 2 {
		t.Fatalf("Test() = %+v, %v, targets=%v", result, err, targets)
	}
	for _, target := range targets {
		u, _ := url.Parse(target)
		if u.Query().Has("q") || u.Query().Get("limit") != "1" {
			t.Fatalf("unexpected test target: %s", target)
		}
	}
}

func TestCancellationAndSafeErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := mustClient(t, TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
		t.Fatal("transport called after context cancellation")
		return nil, nil
	}))
	_, err := client.Search(ctx, models.NetBoxDevice, "ab", 1, 0)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "ab") {
		t.Fatalf("unexpected cancellation error: %v", err)
	}
	apiClient := mustClient(t, TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
		return jsonResponse(404, `secret remote body`), nil
	}))
	_, err = apiClient.GetObject(context.Background(), models.NetBoxDevice, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected API error: %v", err)
	}
}

func TestSearchInputValidation(t *testing.T) {
	t.Parallel()
	client := mustClient(t, TransportFunc(func(context.Context, string, string, []byte, map[string]string) (*Response, error) {
		t.Fatal("transport called for invalid input")
		return nil, nil
	}))
	for _, args := range []struct {
		q             string
		limit, offset int
	}{{"x", 1, 0}, {"ab", 51, 0}, {"ab", 1, 1001}, {strings.Repeat("ü", 201), 1, 0}} {
		_, err := client.Search(context.Background(), models.NetBoxDevice, args.q, args.limit, args.offset)
		var validation *ValidationError
		if !errors.As(err, &validation) {
			t.Errorf("Search(%q,%d,%d) error = %T", args.q, args.limit, args.offset, err)
		}
	}
}

func mustClient(t *testing.T, transport Transport) *Client {
	t.Helper()
	client, err := NewClient("https://netbox.example/root", "nbt_key.token", models.NetBoxAuthBearer, transport)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func jsonResponse(status int, body string) *Response {
	return &Response{StatusCode: status, Body: []byte(body)}
}
