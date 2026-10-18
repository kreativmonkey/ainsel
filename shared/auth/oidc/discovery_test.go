package oidc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"jwks_uri": "https://idp.example.com/oauth/v2/keys",
			"userinfo_endpoint": "https://idp.example.com/oidc/v1/userinfo"
		}`, "http://"+r.Host)
	}))
	t.Cleanup(srv.Close)

	d, err := FetchDiscovery(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("FetchDiscovery: %v", err)
	}
	if d.JWKSURI != "https://idp.example.com/oauth/v2/keys" {
		t.Errorf("JWKSURI = %q", d.JWKSURI)
	}
	if d.UserInfoEndpoint != "https://idp.example.com/oidc/v1/userinfo" {
		t.Errorf("UserInfoEndpoint = %q", d.UserInfoEndpoint)
	}
}

func TestFetchDiscoveryStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	if _, err := FetchDiscovery(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for 404 discovery document")
	}
}

func TestFetchDiscoveryInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(srv.Close)

	if _, err := FetchDiscovery(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFetchDiscoveryMissingJWKS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"issuer":%q}`, "http://"+r.Host)
	}))
	t.Cleanup(srv.Close)

	if _, err := FetchDiscovery(context.Background(), nil, srv.URL); err == nil {
		t.Fatal("expected error when jwks_uri is missing")
	}
}

func TestResolveEndpointsUsesDiscovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"jwks_uri": "https://idp.example.com/custom/keys",
			"userinfo_endpoint": "https://idp.example.com/custom/userinfo"
		}`, "http://"+r.Host)
	}))
	t.Cleanup(srv.Close)

	jwks, userinfo, err := ResolveEndpoints(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("ResolveEndpoints: %v", err)
	}
	if jwks != "https://idp.example.com/custom/keys" || userinfo != "https://idp.example.com/custom/userinfo" {
		t.Errorf("got jwks=%q userinfo=%q", jwks, userinfo)
	}
}

func TestResolveEndpointsErrorOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	jwks, userinfo, err := ResolveEndpoints(context.Background(), nil, srv.URL)
	if err == nil {
		t.Fatal("expected discovery error to be surfaced")
	}
	if jwks != "" || userinfo != "" {
		t.Errorf("expected empty URLs on failure, got jwks=%q userinfo=%q", jwks, userinfo)
	}
}

func TestResolveEndpointsMissingUserInfoEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{
			"issuer": %q,
			"jwks_uri": "https://idp.example.com/custom/keys"
		}`, "http://"+r.Host)
	}))
	t.Cleanup(srv.Close)

	jwks, userinfo, err := ResolveEndpoints(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatalf("ResolveEndpoints: %v", err)
	}
	if jwks != "https://idp.example.com/custom/keys" {
		t.Errorf("jwks = %q", jwks)
	}
	if userinfo != "" {
		t.Errorf("userinfo = %q; want empty", userinfo)
	}
}

func TestFetchDiscoveryValidatesIssuer(t *testing.T) {
	for _, tc := range []struct{ name, advertised string }{
		{"missing", ""}, {"different-provider", "https://other.test"}, {"trailing-slash-mismatch", "self"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				advertised := tc.advertised
				if advertised == "self" {
					advertised = "http://" + r.Host + "/application/o/app"
				}
				_, _ = fmt.Fprintf(w, `{"issuer":%q,"jwks_uri":"https://provider.test/keys"}`, advertised)
			}))
			defer server.Close()
			if _, err := FetchDiscovery(context.Background(), nil, server.URL+"/application/o/app/"); err == nil {
				t.Fatal("must reject mismatching issuer")
			}
		})
	}
}
