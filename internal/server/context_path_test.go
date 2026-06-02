package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithContextPathStripsPrefix(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/setup/status" {
			t.Fatalf("path = %q, want stripped internal path", r.URL.Path)
		}
		if got := r.Header.Get(contextPathHeader); got != "/windshift" {
			t.Fatalf("%s = %q", contextPathHeader, got)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r := httptest.NewRequest(http.MethodGet, "http://example.test/windshift/api/setup/status?x=1", nil)
	w := httptest.NewRecorder()
	withContextPath(next, "/windshift").ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestWithContextPathRejectsOutsidePrefix(t *testing.T) {
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler should not run")
	})

	for _, path := range []string{"/", "/api/setup/status", "/windshifted/api/setup/status"} {
		r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		w := httptest.NewRecorder()
		withContextPath(next, "/windshift").ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, w.Code)
		}
	}
}

func TestWithContextPathRedirectsBarePrefix(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://example.test/windshift?x=1", nil)
	w := httptest.NewRecorder()
	withContextPath(http.NotFoundHandler(), "/windshift").ServeHTTP(w, r)

	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/windshift/?x=1" {
		t.Fatalf("Location = %q", got)
	}
}

func TestPrepareIndexHTMLPrefixesRootAssetsAndInjectsContextPath(t *testing.T) {
	input := []byte(`<!doctype html><head><script>theme()</script><script type="module" src="/_app/app.js"></script><link href="/_app/app.css"><link href="/windshift-3.svg"></head><body></body>`)
	got := string(prepareIndexHTML(input, "nonce123", "/windshift"))

	for _, want := range []string{
		`<script nonce="nonce123">theme()</script>`,
		`window.__WINDSHIFT_CONTEXT_PATH__="/windshift"`,
		`src="/windshift/_app/app.js"`,
		`href="/windshift/_app/app.css"`,
		`href="/windshift/windshift-3.svg"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prepared HTML missing %q in %s", want, got)
		}
	}
}
