package server

import (
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
		next.ServeHTTP(w, r2)
	})
}
