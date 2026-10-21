package oidc

import (
	"os"
	"strings"
)

// FromEnvironment reads the common OIDC settings for every AInsel service.
// The Zitadel variable names remain aliases for existing installations, and
// an installation configured through them keeps the Zitadel roles claim when
// OIDC_ROLES_CLAIM is unset.
func FromEnvironment() Config {
	legacyZitadel := false
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		issuer = os.Getenv("ZITADEL_ISSUER")
		legacyZitadel = issuer != ""
	}
	if os.Getenv("OIDC_PROJECT_ID") != "" {
		legacyZitadel = true
	}
	audience := os.Getenv("OIDC_AUDIENCE")
	if audience == "" {
		audience = os.Getenv("OIDC_PROJECT_ID")
	}
	rolesClaim := os.Getenv("OIDC_ROLES_CLAIM")
	if rolesClaim == "" && legacyZitadel {
		rolesClaim = zitadelRolesClaim
	}
	if audience == "" {
		audience = os.Getenv("OIDC_CLIENT_ID")
	}
	var signingAlgs []string
	if value, ok := os.LookupEnv("OIDC_SIGNING_ALGS"); ok {
		signingAlgs = strings.Split(value, ",")
		for i := range signingAlgs {
			signingAlgs[i] = strings.TrimSpace(signingAlgs[i])
		}
	}
	return Config{Issuer: issuer, Audience: audience, RolesClaim: rolesClaim, SigningAlgs: signingAlgs}
}
