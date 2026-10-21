package oidc_test

import (
	"testing"

	"github.com/ainsel-project/ainsel/shared/auth/oidc"
)

func TestFromEnvironment(t *testing.T) {
	for _, name := range []string{"OIDC_ISSUER", "ZITADEL_ISSUER", "OIDC_AUDIENCE", "OIDC_PROJECT_ID", "OIDC_CLIENT_ID", "OIDC_ROLES_CLAIM"} {
		t.Setenv(name, "")
	}
	t.Setenv("ZITADEL_ISSUER", "legacy")
	t.Setenv("OIDC_PROJECT_ID", "project")
	if cfg := oidc.FromEnvironment(); cfg.Issuer != "legacy" || cfg.Audience != "project" || cfg.RolesClaim != "urn:zitadel:iam:org:project:roles" {
		t.Fatalf("legacy aliases: %+v", cfg)
	}
	t.Setenv("OIDC_ISSUER", "https://idp.test/application/o/app/")
	t.Setenv("OIDC_AUDIENCE", "app")
	t.Setenv("OIDC_ROLES_CLAIM", "groups")
	if cfg := oidc.FromEnvironment(); cfg.Issuer != "https://idp.test/application/o/app/" || cfg.Audience != "app" || cfg.RolesClaim != "groups" {
		t.Fatalf("generic settings must take precedence: %+v", cfg)
	}
	t.Setenv("OIDC_AUDIENCE", "")
	t.Setenv("OIDC_PROJECT_ID", "")
	t.Setenv("OIDC_CLIENT_ID", "client")
	if cfg := oidc.FromEnvironment(); cfg.Audience != "client" {
		t.Fatalf("client audience fallback: %+v", cfg)
	}
	t.Setenv("OIDC_ROLES_CLAIM", "")
	if cfg := oidc.FromEnvironment(); cfg.RolesClaim != "" {
		t.Fatalf("generic settings must not select the Zitadel roles claim: %+v", cfg)
	}
	t.Setenv("OIDC_PROJECT_ID", "project")
	if cfg := oidc.FromEnvironment(); cfg.RolesClaim != "urn:zitadel:iam:org:project:roles" {
		t.Fatalf("legacy project ID must keep the Zitadel roles claim: %+v", cfg)
	}
}
