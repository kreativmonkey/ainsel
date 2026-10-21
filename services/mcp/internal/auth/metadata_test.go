package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ainsel-project/ainsel/services/mcp/internal/auth"
)

func TestProtectedResourceMetadataHandler(t *testing.T) {
	h, err := auth.ProtectedResourceMetadataHandler(auth.MetadataConfig{
		ResourceURL: "https://mcp.test/ainsel-dev/mcp",
		Issuer:      "https://auth.test",
		Scopes:      "openid profile email offline_access custom-scope",
	})
	if err != nil {
		t.Fatalf("ProtectedResourceMetadataHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", got)
	}

	var body struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
		ScopesSupported        []string `json:"scopes_supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json.Unmarshal: %v; body = %s", err, rec.Body.String())
	}
	if body.Resource != "https://mcp.test/ainsel-dev/mcp" {
		t.Errorf("resource = %q; want https://mcp.test/ainsel-dev/mcp", body.Resource)
	}
	// Clients discover the actual configured provider, without a vendor-specific proxy.
	if len(body.AuthorizationServers) != 1 || body.AuthorizationServers[0] != "https://auth.test" {
		t.Errorf("authorization_servers = %v; want [https://auth.test]", body.AuthorizationServers)
	}
	wantScope := "custom-scope"
	found := false
	for _, s := range body.ScopesSupported {
		if s == wantScope {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("scopes_supported = %v; missing %q", body.ScopesSupported, wantScope)
	}
}

func TestProtectedResourceMetadataLocation(t *testing.T) {
	cases := []struct {
		name     string
		resource string
		wantPath string
		wantURL  string
		wantErr  bool
	}{
		{name: "root", resource: "https://mcp.test", wantPath: "/.well-known/oauth-protected-resource", wantURL: "https://mcp.test/.well-known/oauth-protected-resource"},
		{name: "path", resource: "https://mcp.test/ainsel/mcp/", wantPath: "/.well-known/oauth-protected-resource/ainsel/mcp", wantURL: "https://mcp.test/.well-known/oauth-protected-resource/ainsel/mcp"},
		{name: "http with port", resource: "http://127.0.0.1:8080/mcp", wantPath: "/.well-known/oauth-protected-resource/mcp", wantURL: "http://127.0.0.1:8080/.well-known/oauth-protected-resource/mcp"},
		{name: "relative", resource: "/mcp", wantErr: true},
		{name: "unsupported scheme", resource: "ftp://mcp.test/mcp", wantErr: true},
		{name: "query", resource: "https://mcp.test/mcp?x=1", wantErr: true},
		{name: "fragment", resource: "https://mcp.test/mcp#x", wantErr: true},
		{name: "user", resource: "https://user@mcp.test/mcp", wantErr: true},
		{name: "user and password", resource: (&url.URL{Scheme: "https", User: url.UserPassword("user", "x"), Host: "mcp.test", Path: "/mcp"}).String(), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, metadataURL, err := auth.ProtectedResourceMetadataLocation(tc.resource)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got path %q url %q", path, metadataURL)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tc.wantPath || metadataURL != tc.wantURL {
				t.Fatalf("got (%q, %q), want (%q, %q)", path, metadataURL, tc.wantPath, tc.wantURL)
			}
		})
	}
}
