package llm

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestJoinProviderPathAvoidsDuplicateOpenAIVersion(t *testing.T) {
	tests := []struct {
		name string
		base string
		path string
		want string
	}{
		{
			name: "root plus openai chat path",
			base: "http://localhost:11434",
			path: "/v1/chat/completions",
			want: "http://localhost:11434/v1/chat/completions",
		},
		{
			name: "versioned base plus openai chat path",
			base: "http://localhost:11434/v1",
			path: "/v1/chat/completions",
			want: "http://localhost:11434/v1/chat/completions",
		},
		{
			name: "versioned base with trailing slash",
			base: "http://localhost:11434/v1/",
			path: "v1/models",
			want: "http://localhost:11434/v1/models",
		},
		{
			name: "non v1 provider path is preserved",
			base: "https://api.z.ai/api/paas/v4",
			path: "/chat/completions",
			want: "https://api.z.ai/api/paas/v4/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinProviderPath(tt.base, tt.path); got != tt.want {
				t.Fatalf("joinProviderPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderModelsURLForVersionedCustomBase(t *testing.T) {
	provider := ProviderInfo{ModelsEndpoint: "/v1/models", BaseURL: "https://api.openai.com"}
	got := provider.ModelsURLForBase("http://localhost:1234/v1")
	want := "http://localhost:1234/v1/models"
	if got != want {
		t.Fatalf("ModelsURLForBase() = %q, want %q", got, want)
	}
}

func TestDefaultOpenRouterHasSeedModels(t *testing.T) {
	LoadDefaultProviders()
	provider := GetProvider(ProviderType("openrouter"))
	if provider == nil {
		t.Fatal("OpenRouter provider not registered")
	}
	if len(provider.Models) == 0 {
		t.Fatal("OpenRouter should include seed models before the admin refreshes the live catalog")
	}
}

func TestAdminConfiguredHTTPClientBlocksLocalhostByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := newAdminConfiguredHTTPClient(time.Second, nil)
	resp, err := client.Get(server.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected default LLM HTTP client to block loopback/private endpoints")
	}
}

func TestAdminConfiguredHTTPClientAllowsLocalhostWithOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	_, loopback, err := net.ParseCIDR("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	client := newAdminConfiguredHTTPClient(time.Second, []*net.IPNet{loopback})
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("expected override LLM HTTP client to allow loopback endpoint: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
