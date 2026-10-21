package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/localauth"
)

// Tests for issue #117: disable/enable users via PATCH /api/v1/users/{id}.

func seedUser(t *testing.T, store *fakeAuthzStore, id, username, password string, isAdmin bool) {
	t.Helper()
	hash, err := localauth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateLocalUser(context.Background(), id, "", username, hash, isAdmin); err != nil {
		t.Fatal(err)
	}
}

func patchUser(t *testing.T, s *Server, id string, body map[string]any, callerSub string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, s, http.MethodPatch, "/api/v1/users/"+id, body, asUser(callerSub, callerSub))
}

func TestPatchUser_DisableEnableRoundTrip(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)
	seedUser(t, store, "local:alice", "alice", "alice-password", false)

	rec := patchUser(t, s, "local:alice", map[string]any{"disabled": true}, "local:admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var u struct {
		ID       string `json:"id"`
		Disabled bool   `json:"disabled"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if u.ID != "local:alice" || !u.Disabled {
		t.Fatalf("disable response: %+v", u)
	}
	if got, err := store.GetUser(context.Background(), "local:alice"); err != nil || !got.Disabled {
		t.Fatalf("store after disable: %+v, %v", got, err)
	}

	rec = patchUser(t, s, "local:alice", map[string]any{"disabled": false}, "local:admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("enable: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if u.Disabled {
		t.Fatalf("enable response still disabled: %+v", u)
	}
}

func TestPatchUser_DisableUnknownUser(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)

	rec := patchUser(t, s, "local:ghost", map[string]any{"disabled": true}, "local:admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestPatchUser_SelfDisableBlocked(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)
	seedUser(t, store, "local:bob", "bob", "bob-password", true)

	// Admin bob cannot disable his own account (mirrors self-deletion).
	rec := patchUser(t, s, "local:bob", map[string]any{"disabled": true}, "local:bob")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if got, err := store.GetUser(context.Background(), "local:bob"); err != nil || got.Disabled {
		t.Fatalf("bob must stay enabled: %+v, %v", got, err)
	}
}

func TestPatchUser_DisableNonAdminRequiresAdmin(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)
	seedUser(t, store, "local:alice", "alice", "alice-password", false)

	// A non-admin caller gets 403, and the flag stays untouched.
	rec := patchUser(t, s, "local:admin", map[string]any{"disabled": true}, "local:alice")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got, err := store.GetUser(context.Background(), "local:admin"); err != nil || got.Disabled {
		t.Fatalf("admin must stay enabled: %+v, %v", got, err)
	}
}

func TestPatchUser_DisableSecondAdminAllowed(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)
	seedUser(t, store, "local:bob", "bob", "bob-password", true)

	// Disabling another admin is allowed while more than one admin exists.
	rec := patchUser(t, s, "local:bob", map[string]any{"disabled": true}, "local:admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got, err := store.GetUser(context.Background(), "local:bob"); err != nil || !got.Disabled {
		t.Fatalf("bob must be disabled: %+v, %v", got, err)
	}
}

func TestLogin_DisabledUserGetsGeneric401(t *testing.T) {
	s, store := localAuthTestServer(t)
	seedAdmin(t, store)

	// Correct credentials, but the account is disabled.
	if err := store.SetDisabled(context.Background(), "local:admin", true); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, s, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "admin-password",
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %s)", rec.Code, rec.Body.String())
	}
	// Byte-identical to the wrong-password response: no enumeration signal.
	wrong := doJSON(t, s, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "wrong-password",
	}, nil)
	if rec.Code != wrong.Code || rec.Body.String() != wrong.Body.String() {
		t.Fatalf("disabled response %q differs from wrong-password response %q", rec.Body.String(), wrong.Body.String())
	}

	// Re-enabling restores login.
	if err := store.SetDisabled(context.Background(), "local:admin", false); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"username": "admin", "password": "admin-password",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("after enable: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
