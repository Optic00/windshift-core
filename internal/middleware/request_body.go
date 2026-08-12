package middleware

import (
	"mime"
	"net/http"
	"strings"

	"windshift/internal/restapi"
)

// LimitJSONRequestBody caps JSON requests before they reach an API handler.
// Paths with a larger handler-specific cap can be exempted by prefix.
func LimitJSONRequestBody(maxBytes int64, exemptPrefixes ...string) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		panic("JSON request body limit must be positive")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, prefix := range exemptPrefixes {
				if strings.HasPrefix(r.URL.Path, prefix) {
					next.ServeHTTP(w, r)
					return
				}
			}

			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
				next.ServeHTTP(w, r)
				return
			}

			if r.ContentLength > maxBytes {
				restapi.RespondError(w, r, restapi.NewAPIError(
					http.StatusRequestEntityTooLarge,
					restapi.ErrCodeRequestTooLarge,
					"Request body too large",
				))
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
