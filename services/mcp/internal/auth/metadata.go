package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// MetadataConfig is the input for ProtectedResourceMetadataHandler.
type MetadataConfig struct {
	ResourceURL string // Externally-reachable MCP URL, e.g. https://ainsel.example.com/mcp
	Issuer      string // Actual OIDC authorization server issuer
	Scopes      string // Space-separated scopes; defaults to standard OIDC scopes
}

// ProtectedResourceMetadataHandler serves /.well-known/oauth-protected-resource
// per RFC 9728. Claude Code's MCP HTTP transport reads this to discover the
// authorization server and start the OAuth flow.
func ProtectedResourceMetadataHandler(cfg MetadataConfig) (http.Handler, error) {
	body := struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
		ScopesSupported        []string `json:"scopes_supported"`
	}{
		Resource:               cfg.ResourceURL,
		AuthorizationServers:   []string{cfg.Issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        strings.Fields(cfg.Scopes),
	}
	if len(body.ScopesSupported) == 0 {
		body.ScopesSupported = []string{"openid", "profile", "email", "offline_access"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("metadata encode: %w", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}), nil
}
