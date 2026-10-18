package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Discovery holds the subset of the OIDC discovery document
// (RFC 8414 / OpenID Connect Discovery 1.0) that the middleware needs to
// locate the issuer's endpoints.
type Discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

// FetchDiscovery retrieves and parses the OIDC discovery document at
// {issuer}/.well-known/openid-configuration. Reading the endpoint URLs from
// discovery (instead of hardcoding provider-specific paths) is what makes
// the middleware work with any compliant IdP — Zitadel, Keycloak, Entra ID,
// Authelia, Okta, etc.
func FetchDiscovery(ctx context.Context, client *http.Client, issuer string) (*Discovery, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	url := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: %s returned status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: read body: %w", err)
	}

	var d Discovery
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("oidc discovery: parse: %w", err)
	}
	if d.Issuer != issuer {
		return nil, fmt.Errorf("oidc discovery: issuer %q does not match configured issuer %q", d.Issuer, issuer)
	}
	if d.JWKSURI == "" {
		return nil, fmt.Errorf("oidc discovery: jwks_uri missing in %s", url)
	}
	return &d, nil
}

// ResolveEndpoints returns the JWKS and userinfo endpoint URLs for an
// issuer from the OIDC discovery document. If discovery fails it returns
// empty URLs and the discovery error; callers must treat the error as fatal
// rather than proceeding with unresolved endpoints.
func ResolveEndpoints(ctx context.Context, client *http.Client, issuer string) (jwksURL, userInfoURL string, err error) {
	d, err := FetchDiscovery(ctx, client, issuer)
	if err != nil {
		return "", "", err
	}
	return d.JWKSURI, d.UserInfoEndpoint, nil
}
