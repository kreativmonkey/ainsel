package authz_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/authz"
	"github.com/ainsel-project/ainsel/shared/auth/oidc"
)

type fakeDisabledStore struct {
	disabled map[string]bool
	err      error
}

func (f fakeDisabledStore) IsUserDisabled(_ context.Context, id string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	d, ok := f.disabled[id]
	if !ok {
		return false, authz.ErrNotFound
	}
	return d, nil
}

func guardTestServer(t *testing.T, store authz.DisabledUserChecker) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	guard := authz.NewDisabledGuard(store)
	mux.Handle("/api/v1/thing", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	return mux
}

func doGuard(mux *http.ServeMux, sub string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	if sub != "" {
		req = req.WithContext(oidc.ContextWithUser(req.Context(), &oidc.User{Sub: sub}))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestDisabledGuard_BlocksDisabledUser(t *testing.T) {
	mux := guardTestServer(t, fakeDisabledStore{disabled: map[string]bool{"alice": true, "bob": false}})
	rec := doGuard(mux, "alice")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user: status = %d, want 401", rec.Code)
	}
	// The body must use the API's JSON error envelope, not http.Error's
	// text/plain, so clients parsing {"error": ...} work on this path too.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if body["error"] != "unauthorized" {
		t.Fatalf("error = %q, want %q", body["error"], "unauthorized")
	}
}

func TestDisabledGuard_PassesEnabledUser(t *testing.T) {
	mux := guardTestServer(t, fakeDisabledStore{disabled: map[string]bool{"alice": true, "bob": false}})
	rec := doGuard(mux, "bob")
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled user: status = %d, want 200", rec.Code)
	}
}

func TestDisabledGuard_PassesUnknownSub(t *testing.T) {
	// First-seen OIDC identities are legitimately not in the registry yet;
	// IdentityPersistMiddleware runs inside the guard and creates them.
	mux := guardTestServer(t, fakeDisabledStore{disabled: map[string]bool{}})
	rec := doGuard(mux, "brand-new-sub")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown sub: status = %d, want 200", rec.Code)
	}
}

func TestDisabledGuard_PassesAnonymous(t *testing.T) {
	// Rejecting anonymous requests is the surrounding closer's job.
	mux := guardTestServer(t, fakeDisabledStore{disabled: map[string]bool{"alice": true}})
	rec := doGuard(mux, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous: status = %d, want 200", rec.Code)
	}
}

func TestDisabledGuard_StoreError(t *testing.T) {
	mux := guardTestServer(t, fakeDisabledStore{err: errors.New("db down")})
	rec := doGuard(mux, "alice")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error: status = %d, want 500", rec.Code)
	}
}
