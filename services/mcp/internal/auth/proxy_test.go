package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ainsel-project/ainsel/services/mcp/internal/auth"
)

func TestOIDCConfigHandler(t *testing.T) {
	cfg := auth.ProxyConfig{
		ProxyIssuer: "https://mcp.test/ainsel-dev/mcp",
		Issuer:      discoveryServer(t),
		ClientID:    "client-123",
	}

	h, err := auth.OIDCConfigHandler(cfg)
	if err != nil {
		t.Fatalf("OIDCConfigHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal: %v; body = %s", err, rec.Body.String())
	}
	for _, endpoint := range []struct{ field, want string }{
		{"authorization_endpoint", "https://idp.test/custom/authorize"},
		{"token_endpoint", "https://idp.test/custom/token"},
		{"jwks_uri", "https://idp.test/custom/keys"},
		{"userinfo_endpoint", "https://idp.test/custom/userinfo"},
	} {
		if body[endpoint.field] != endpoint.want {
			t.Errorf("%s = %v; want %s", endpoint.field, body[endpoint.field], endpoint.want)
		}
	}
	if body["issuer"] != cfg.ProxyIssuer {
		t.Errorf("issuer = %q; want %q", body["issuer"], cfg.ProxyIssuer)
	}
	if body["registration_endpoint"] != cfg.ProxyIssuer+"/oauth/register" {
		t.Errorf("registration_endpoint = %q; want %q", body["registration_endpoint"], cfg.ProxyIssuer+"/oauth/register")
	}
}

func TestOAuthASMetadataHandler(t *testing.T) {
	cfg := auth.ProxyConfig{
		ProxyIssuer: "https://mcp.test/ainsel-dev/mcp",
		Issuer:      discoveryServer(t),
		ClientID:    "client-123",
	}

	h, err := auth.OAuthASMetadataHandler(cfg)
	if err != nil {
		t.Fatalf("OAuthASMetadataHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server/ainsel-dev/mcp", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal: %v; body = %s", err, rec.Body.String())
	}
	for _, endpoint := range []struct{ field, want string }{
		{"authorization_endpoint", "https://idp.test/custom/authorize"},
		{"token_endpoint", "https://idp.test/custom/token"},
		{"jwks_uri", "https://idp.test/custom/keys"},
		{"userinfo_endpoint", "https://idp.test/custom/userinfo"},
	} {
		if body[endpoint.field] != endpoint.want {
			t.Errorf("%s = %v; want %s", endpoint.field, body[endpoint.field], endpoint.want)
		}
	}
	if body["issuer"] != cfg.ProxyIssuer {
		t.Errorf("issuer = %q; want %q", body["issuer"], cfg.ProxyIssuer)
	}
}

func discoveryServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jwks_uri":"https://idp.test/custom/keys","authorization_endpoint":"https://idp.test/custom/authorize","token_endpoint":"https://idp.test/custom/token","userinfo_endpoint":"https://idp.test/custom/userinfo"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestOIDCConfigHandlerDiscoveryFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	if _, err := auth.OIDCConfigHandler(auth.ProxyConfig{Issuer: srv.URL}); err == nil {
		t.Fatal("expected discovery failure")
	}
}
