package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"windshift/internal/restapi"
)

// Recovery returns middleware that recovers from panics and returns a structured error response.
// It logs the panic with stack trace for debugging purposes.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// http.ErrAbortHandler is not a crash: httputil.ReverseProxy
				// panics it to abort a connection whose response body copy
				// failed mid-stream (e.g. an upstream LLM SSE stream dropping
				// under ProxyLLM). The standard net/http server recovers it
				// silently and RSTs the connection. Re-panic so it reaches that
				// handling instead of being logged as a stack-trace "crash" and
				// — worse — triggering a 500 write onto a response that has
				// already streamed bytes to the client.
				if err == http.ErrAbortHandler {
					panic(err)
				}
				// Log the panic with stack trace
				slog.Error("panic recovered", //nolint:gosec // logging panic recovery info for debugging
					slog.Any("error", err),
					slog.String("path", r.URL.Path),
					slog.String("method", r.Method),
					slog.String("stack", string(debug.Stack())),
				)

				// Return structured JSON error response
				restapi.RespondError(w, r, restapi.ErrInternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
