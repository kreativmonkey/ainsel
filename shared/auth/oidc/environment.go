package oidc

import (
	"os"
	"strings"
)

// FromEnvironment reads the common OIDC settings for every AInsel service.
// The Zitadel variable names remain aliases for existing installations.
func FromEnvironment() Config {
	issuer := os.Getenv("OIDC_ISSUER")
	if issuer == "" {
		issuer = os.Getenv("ZITADEL_ISSUER")
	}
	audience := os.Getenv("OIDC_AUDIENCE")
	if audience == "" {
		audience = os.Getenv("OIDC_PROJECT_ID")
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
	return Config{Issuer: issuer, Audience: audience, RolesClaim: os.Getenv("OIDC_ROLES_CLAIM"), SigningAlgs: signingAlgs}
}
