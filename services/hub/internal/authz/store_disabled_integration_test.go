package authz_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/authz"
)

// TestDisabledFlag covers the disable/enable feature (issue #117):
// SetDisabled/IsUserDisabled round-trip and the invariant that OIDC identity
// persistence (UpsertUser) must never resurrect a disabled user.
func TestDisabledFlag(t *testing.T) {
	env := newTestAuthZ(t)
	defer env.cleanup()
	ctx := context.Background()

	if _, err := env.store.CreateLocalUser(ctx, "local:alice", "alice@example.com", "alice", "$argon2id$fake", false); err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}

	// New users default to enabled.
	u, err := env.store.GetUser(ctx, "local:alice")
	if err != nil || u.Disabled {
		t.Fatalf("fresh user: Disabled = %v, err = %v", u.Disabled, err)
	}
	if d, err := env.store.IsUserDisabled(ctx, "local:alice"); err != nil || d {
		t.Fatalf("IsUserDisabled(fresh) = %v, %v", d, err)
	}

	// Disable round-trip.
	if err := env.store.SetDisabled(ctx, "local:alice", true); err != nil {
		t.Fatalf("SetDisabled(true): %v", err)
	}
	if d, err := env.store.IsUserDisabled(ctx, "local:alice"); err != nil || !d {
		t.Fatalf("IsUserDisabled(disabled) = %v, %v", d, err)
	}
	u, err = env.store.GetUser(ctx, "local:alice")
	if err != nil || !u.Disabled {
		t.Fatalf("GetUser after disable: %+v, %v", u, err)
	}
	users, err := env.store.ListUsers(ctx)
	if err != nil || len(users) != 1 || !users[0].Disabled {
		t.Fatalf("ListUsers after disable: %+v, %v", users, err)
	}

	// UpsertUser (OIDC re-persistence) must not clear the disabled flag.
	if _, err := env.store.UpsertUser(ctx, "local:alice", "alice@example.com", "alice"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if d, err := env.store.IsUserDisabled(ctx, "local:alice"); err != nil || !d {
		t.Fatalf("UpsertUser cleared disabled: %v, %v", d, err)
	}
	// A brand-new UpsertUser defaults to enabled.
	if _, err := env.store.UpsertUser(ctx, "oidc:bob", "bob@example.com", "bob"); err != nil {
		t.Fatalf("UpsertUser(bob): %v", err)
	}
	if d, err := env.store.IsUserDisabled(ctx, "oidc:bob"); err != nil || d {
		t.Fatalf("new UpsertUser disabled: %v, %v", d, err)
	}

	// Enable round-trip.
	if err := env.store.SetDisabled(ctx, "local:alice", false); err != nil {
		t.Fatalf("SetDisabled(false): %v", err)
	}
	if d, err := env.store.IsUserDisabled(ctx, "local:alice"); err != nil || d {
		t.Fatalf("IsUserDisabled(enabled) = %v, %v", d, err)
	}

	// Missing user errors.
	if err := env.store.SetDisabled(ctx, "local:ghost", true); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("SetDisabled(ghost): %v, want ErrNotFound", err)
	}
	if _, err := env.store.IsUserDisabled(ctx, "local:ghost"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("IsUserDisabled(ghost): %v, want ErrNotFound", err)
	}
}
