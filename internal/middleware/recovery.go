package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"windshift/internal/restapi"
)

// Recovery logs panics and returns structured internal errors.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				// Let net/http reset aborted streamed responses without writing a 500.
				if err == http.ErrAbortHandler {
					panic(err)
				}
				slog.Error("panic recovered", //nolint:gosec // logging panic recovery info for debugging
					slog.Any("error", err),
					slog.String("path", r.URL.Path),
					slog.String("method", r.Method),
					slog.String("stack", string(debug.Stack())),
				)

				restapi.RespondError(w, r, restapi.ErrInternalError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
