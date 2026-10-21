package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/api"
)

func TestWireMetricsSource(t *testing.T) {
	// The chart value and the response field are meant to read the same, so the
	// accepted tokens are the api package's, not a second spelling invented here.
	// An unrecognised value must land on the default rather than fail startup:
	// this setting chooses between two backends that both work, so a typo costs a
	// log line, not a pod.
	cases := []struct {
		raw, want string
	}{
		{"", api.MetricsSourceRecords},
		{"postgres", api.MetricsSourceRecords},
		{"POSTGRES", api.MetricsSourceRecords},
		{"  postgres  ", api.MetricsSourceRecords},
		{"prometheus", api.MetricsSourcePrometheus},
		{"Prometheus", api.MetricsSourcePrometheus},
		{"prometheus ", api.MetricsSourcePrometheus},
		{"victoriametrics", api.MetricsSourceRecords},
		{"prom", api.MetricsSourceRecords},
		{"records", api.MetricsSourceRecords},
	}
	for _, tc := range cases {
		if got := wireMetricsSource(tc.raw); got != tc.want {
			t.Errorf("wireMetricsSource(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// TestWireAuthMiddlewareOIDCPlusLocalGuards covers the startup guards for
// the oidc+local coexistence mode (issue #114). Only the error paths are
// exercised: they all return before any network or container access, so no
// live OIDC issuer or database is needed.
func TestWireAuthMiddlewareOIDCPlusLocalGuards(t *testing.T) {
	setEnv := func(t *testing.T, key, val string) {
		t.Setenv(key, val)
	}

	t.Run("invalid mode error lists oidc+local", func(t *testing.T) {
		setEnv(t, "HUB_AUTH_MODE", "bogus")
		setEnv(t, "OIDC_ISSUER", "")
		setEnv(t, "OIDC_AUDIENCE", "")
		err := wireAuthMiddleware(context.Background(), &container{}, containerConfig{})
		if err == nil || !strings.Contains(err.Error(), "oidc+local") {
			t.Fatalf("expected invalid-mode error to mention oidc+local, got: %v", err)
		}
	})

	t.Run("oidc+local requires issuer and audience", func(t *testing.T) {
		setEnv(t, "HUB_AUTH_MODE", "oidc+local")
		setEnv(t, "OIDC_ISSUER", "")
		setEnv(t, "OIDC_AUDIENCE", "")
		setEnv(t, "HUB_LOCAL_JWT_SECRET", "0123456789abcdef0123456789abcdef")
		err := wireAuthMiddleware(context.Background(), &container{}, containerConfig{})
		if err == nil || !strings.Contains(err.Error(), "OIDC_ISSUER") {
			t.Fatalf("expected issuer/audience requirement error, got: %v", err)
		}
	})

	t.Run("oidc+local requires the local JWT secret", func(t *testing.T) {
		setEnv(t, "HUB_AUTH_MODE", "oidc+local")
		setEnv(t, "OIDC_ISSUER", "https://idp.test/")
		setEnv(t, "OIDC_AUDIENCE", "ainsel")
		setEnv(t, "HUB_LOCAL_JWT_SECRET", "")
		err := wireAuthMiddleware(context.Background(), &container{}, containerConfig{})
		if err == nil || !strings.Contains(err.Error(), "HUB_LOCAL_JWT_SECRET") {
			t.Fatalf("expected HUB_LOCAL_JWT_SECRET requirement error, got: %v", err)
		}
	})
}
