// Package auth provides a thin adapter that constructs the shared OIDC
// middleware for the MCP server.
package auth

import (
	"net/http"

	"github.com/ainsel-project/ainsel/shared/auth/oidc"
)

// Config configures the MCP auth middleware.
type Config struct {
	Issuer              string   // OIDC issuer URL (e.g. https://oidc.example.com)
	Audience            string   // Required value in token aud
	RolesClaim          string   // Optional roles/groups claim
	SigningAlgs         []string // Allowed JWT signing algorithms; nil defaults to RS256
	ResourceMetadataURL string   // Optional public RFC 9728 metadata URL

	// JWKSURL and UserInfoURL are the resolved OIDC endpoints. Callers must
	// resolve them via oidc.ResolveEndpoints (OIDC discovery); they are not
	// derived from Issuer. An empty JWKSURL is rejected by the shared
	// middleware.
	JWKSURL     string
	UserInfoURL string
}

// NewMiddleware constructs the OIDC middleware. Returns 401 with a plain
// Bearer challenge on failure, with an optional RFC 9728 metadata link.
func NewMiddleware(cfg Config) (func(http.Handler) http.Handler, error) {
	challenge := `Bearer error="invalid_token"`
	if cfg.ResourceMetadataURL != "" {
		challenge += `, resource_metadata="` + cfg.ResourceMetadataURL + `"`
	}
	return oidc.NewMiddleware(oidc.Config{
		Issuer:          cfg.Issuer,
		Audience:        cfg.Audience,
		RolesClaim:      cfg.RolesClaim,
		SigningAlgs:     cfg.SigningAlgs,
		JWKSURL:         cfg.JWKSURL,
		UserInfoURL:     cfg.UserInfoURL,
		WWWAuthenticate: challenge,
	})
}
