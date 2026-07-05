package server

import (
	"net/http/httptest"
	"testing"
)

func TestValidateInternalTokenRequiresBearerPrefix(t *testing.T) {
	const secret = "shared-secret"

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "valid bearer", header: "Bearer " + secret, want: true},
		{name: "wrong prefix same length", header: "Token: " + secret, want: false},
		{name: "missing space", header: "Bearer" + secret, want: false},
		{name: "wrong secret", header: "Bearer nope", want: false},
		{name: "empty", header: "", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/internal/llm", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if got := validateInternalToken(req, secret); got != tc.want {
				t.Fatalf("validateInternalToken() = %v, want %v", got, tc.want)
			}
		})
	}
}
