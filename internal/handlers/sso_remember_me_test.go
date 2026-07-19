package handlers

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSSORememberMeConfiguration(t *testing.T) {
	if ssoStateTokenTTL != 15*time.Minute {
		t.Fatalf("ssoStateTokenTTL = %v, want 15m", ssoStateTokenTTL)
	}

	request := httptest.NewRequest("GET", "/api/sso/acme/login?remember_me=true", nil)
	if !ssoRememberMeFromRequest(request) {
		t.Fatal("remember_me=true was not recognized")
	}
}
