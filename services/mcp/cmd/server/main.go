package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ainsel-project/ainsel/services/mcp/internal/auth"
	"github.com/ainsel-project/ainsel/services/mcp/internal/config"
	mcpserver "github.com/ainsel-project/ainsel/services/mcp/internal/mcp"
	"github.com/ainsel-project/ainsel/shared/auth/oidc"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	if err := cfg.Validate(); err != nil {
		log.Error("config invalid", "error", err)
		os.Exit(1)
	}

	// Resolve JWKS/userinfo endpoints from the issuer's OIDC discovery
	// document so we do not depend on provider-specific paths (Zitadel
	// moved userinfo from /oauth/v2/userinfo to /oidc/v1/userinfo).
	// There is no fallback: a broken issuer must fail startup visibly.
	discoveryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	jwksURL, userInfoURL, derr := oidc.ResolveEndpoints(discoveryCtx, nil, cfg.OIDCIssuer)
	cancel()
	if derr != nil {
		log.Error("OIDC discovery failed", "issuer", cfg.OIDCIssuer, "error", derr)
		os.Exit(1)
	}
	log.Info("OIDC endpoints resolved via discovery", "jwks", jwksURL, "userinfo", userInfoURL)

	metadataURL := ""
	metadataPath := "/.well-known/oauth-protected-resource"
	if cfg.MCPResourceURL != "" {
		resource, err := url.Parse(cfg.MCPResourceURL)
		if err != nil || resource.Host == "" || (resource.Scheme != "https" && resource.Scheme != "http") || resource.RawQuery != "" || resource.Fragment != "" {
			log.Error("MCP_RESOURCE_URL must be an absolute HTTP(S) URL without query or fragment")
			os.Exit(1)
		}
		metadataPath += strings.TrimRight(resource.Path, "/")
		metadataURL = resource.Scheme + "://" + resource.Host + metadataPath
	}
	oidcMW, err := auth.NewMiddleware(auth.Config{
		Issuer:              cfg.OIDCIssuer,
		Audience:            cfg.OIDCAudience,
		RolesClaim:          cfg.OIDCRolesClaim,
		SigningAlgs:         cfg.OIDCSigningAlgs,
		ResourceMetadataURL: metadataURL,
		JWKSURL:             jwksURL,
		UserInfoURL:         userInfoURL,
	})
	if err != nil {
		log.Error("auth middleware init", "error", err)
		os.Exit(1)
	}
	log.Info("OIDC auth enabled", "issuer", cfg.OIDCIssuer, "audience", cfg.OIDCAudience)

	mw := auth.UserTokenMiddleware(cfg.HubURL, cfg.InternalValidateSecret, oidcMW)
	if cfg.InternalValidateSecret != "" {
		log.Info("user token auth enabled")
	} else {
		log.Warn("INTERNAL_VALIDATE_SECRET not set, user token auth disabled")
	}

	s := mcpserver.New(log, mcpserver.Backends{
		HubURL: cfg.HubURL,
	})
	handler := mcpserver.StreamableHTTPHandler(s)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, `{"status":"ok"}`)
	})
	mux.Handle("/mcp", mw(handler))
	if cfg.MCPResourceURL != "" {
		metadata, err := auth.ProtectedResourceMetadataHandler(auth.MetadataConfig{
			ResourceURL: cfg.MCPResourceURL,
			Issuer:      cfg.OIDCIssuer,
			Scopes:      os.Getenv("OIDC_SCOPES"),
		})
		if err != nil {
			log.Error("OAuth protected resource metadata", "error", err)
			os.Exit(1)
		}
		mux.Handle(metadataPath, metadata)
		if metadataPath != "/.well-known/oauth-protected-resource" {
			mux.Handle("/.well-known/oauth-protected-resource", metadata)
		}
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Info("starting ainsel-mcp", "addr", addr)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Info("shutting down")
		_ = srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}
