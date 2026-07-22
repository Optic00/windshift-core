package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
)

const contextPathHeader = "X-Windshift-Context-Path"

// withContextPath translates an externally visible context path (for example
// /windshift) into Windshift's internal root-relative route tree. Existing
// handlers continue to see /api, /rest, /workspaces, /_app, etc.; callers only
// see /windshift/api, /windshift/rest, /windshift/workspaces, /windshift/_app.
func withContextPath(next http.Handler, contextPath string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contextPath == "" {
			r.Header.Del(contextPathHeader)
			next.ServeHTTP(w, r)
			return
		}

		if r.URL.Path == contextPath {
			target := contextPath + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			w.Header().Set("Location", target)
			w.WriteHeader(http.StatusPermanentRedirect)
			return
		}

		// RFC 8414 inserts an authorization server issuer path after the
		// well-known prefix instead of before it. RFC 9728 clients may apply
		// the same path-aware fallback to a protected resource. Map those two
		// standardized discovery forms into the internal route tree while all
		// ordinary Windshift routes remain strictly context-prefixed.
		if internalPath, ok := contextPathWellKnownRoute(r.URL.Path, contextPath); ok {
			r2 := r.Clone(r.Context())
			r2.URL.Path = internalPath
			r2.URL.RawPath = ""
			r2.Header = r.Header.Clone()
			r2.Header.Set(contextPathHeader, contextPath)
			if r2.Header.Get("X-Forwarded-Prefix") == "" {
				r2.Header.Set("X-Forwarded-Prefix", contextPath)
			}
			next.ServeHTTP(&contextPathResponseWriter{ResponseWriter: w, contextPath: contextPath}, r2)
			return
		}

		if !strings.HasPrefix(r.URL.Path, contextPath+"/") {
			http.NotFound(w, r)
			return
		}

		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(r.URL.Path, contextPath)
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		// RawPath is optional and easy to get wrong after prefix stripping. Clear
		// it so net/http and downstream handlers rely on the normalized Path.
		r2.URL.RawPath = ""
		r2.Header = r.Header.Clone()
		r2.Header.Del(contextPathHeader)
		r2.Header.Set(contextPathHeader, contextPath)
		if r2.Header.Get("X-Forwarded-Prefix") == "" {
			r2.Header.Set("X-Forwarded-Prefix", contextPath)
		}
		// Downstream handlers issue redirects to root-relative SPA routes
		// (e.g. /?sso_error=..., /profile?oauth=success). Those Location
		// headers are followed directly by the browser and never pass through
		// the SPA's URL-translation layer, so rewrite them on the way out to
		// carry the context-path prefix.
		next.ServeHTTP(&contextPathResponseWriter{ResponseWriter: w, contextPath: contextPath}, r2)
	})
}

func contextPathWellKnownRoute(path, contextPath string) (string, bool) {
	switch path {
	case "/.well-known/oauth-authorization-server" + contextPath:
		return "/.well-known/oauth-authorization-server", true
	case "/.well-known/oauth-protected-resource" + contextPath + "/mcp":
		return "/.well-known/oauth-protected-resource/mcp", true
	default:
		return "", false
	}
}

// contextPathResponseWriter prefixes the context path onto root-relative
// redirect Location headers emitted by downstream handlers.
type contextPathResponseWriter struct {
	http.ResponseWriter
	contextPath string
	wroteHeader bool
}

func (w *contextPathResponseWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if code >= http.StatusMultipleChoices && code < http.StatusBadRequest {
			if loc := w.Header().Get("Location"); loc != "" {
				if rewritten := prefixLocation(loc, w.contextPath); rewritten != loc {
					w.Header().Set("Location", rewritten)
				}
			}
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *contextPathResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush and Hijack preserve the streaming / connection-upgrade capabilities of
// the wrapped writer (gzhttp, behind this middleware, type-asserts for them).
func (w *contextPathResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *contextPathResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("server: underlying ResponseWriter does not support hijacking")
}

// Unwrap exposes the wrapped writer so http.NewResponseController can reach the
// underlying connection. Without this, SSE handlers' unbindStreamDeadlines would
// silently no-op under a context path and the 30s WriteTimeout would sever every
// stream (WI-484).
func (w *contextPathResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// prefixLocation prepends the context path to a root-relative, same-origin
// Location target. Absolute URLs, protocol-relative URLs, and targets already
// under the context path are returned unchanged.
func prefixLocation(loc, contextPath string) string {
	if contextPath == "" || loc == "" {
		return loc
	}
	if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
		return loc
	}
	if loc == contextPath || strings.HasPrefix(loc, contextPath+"/") {
		return loc
	}
	return contextPath + loc
}
