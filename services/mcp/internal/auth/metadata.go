package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// MetadataConfig is the input for ProtectedResourceMetadataHandler.
type MetadataConfig struct {
	ResourceURL string // Externally-reachable MCP URL, e.g. https://ainsel.example.com/mcp
	Issuer      string // Actual OIDC authorization server issuer
	Scopes      string // Space-separated scopes; defaults to standard OIDC scopes
}

const protectedResourceMetadataPath = "/.well-known/oauth-protected-resource"

// ProtectedResourceMetadataLocation validates the public MCP resource URL and
// returns the RFC 9728 metadata path to serve plus its absolute URL for the
// WWW-Authenticate challenge. The resource must be an absolute HTTP(S) URL
// without credentials, query or fragment.
func ProtectedResourceMetadataLocation(resourceURL string) (path, metadataURL string, err error) {
	resource, err := url.Parse(resourceURL)
	if err != nil || resource.Host == "" || (resource.Scheme != "https" && resource.Scheme != "http") ||
		resource.User != nil || resource.RawQuery != "" || resource.Fragment != "" {
		return "", "", fmt.Errorf("MCP_RESOURCE_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	path = protectedResourceMetadataPath + strings.TrimRight(resource.Path, "/")
	return path, resource.Scheme + "://" + resource.Host + path, nil
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
