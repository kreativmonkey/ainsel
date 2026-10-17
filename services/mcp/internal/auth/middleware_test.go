package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ainsel-project/ainsel/services/mcp/internal/auth"
)

func TestNewMiddlewareRejectsMissingToken_AddsWWWAuthenticate(t *testing.T) {
	// The middleware no longer derives the JWKS URL from the issuer; the
	// test passes the resolved endpoint explicitly.
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(jwksServer.Close)

	mw, err := auth.NewMiddleware(auth.Config{
		Issuer:   jwksServer.URL,
		Audience: "proj-123",
		JWKSURL:  jwksServer.URL + "/keys",
	})
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called on missing token")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if got != `Bearer error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q; want Bearer error=\"invalid_token\"", got)
	}
	if !strings.Contains(rec.Body.String(), "missing bearer token") {
		t.Errorf("body = %q; want contains 'missing bearer token'", rec.Body.String())
	}
}

func TestNewMiddlewareRejectsEmptyJWKSURL(t *testing.T) {
	// No legacy fallback: an empty JWKSURL must error instead of silently
	// constructing the legacy Zitadel path from the issuer.
	_, err := auth.NewMiddleware(auth.Config{
		Issuer:   "https://idp.example.com",
		Audience: "proj-123",
	})
	if err == nil {
		t.Fatal("expected error for empty JWKSURL")
	}
	if !strings.Contains(err.Error(), "JWKSURL is required") {
		t.Errorf("err = %v; want 'JWKSURL is required'", err)
	}
}
