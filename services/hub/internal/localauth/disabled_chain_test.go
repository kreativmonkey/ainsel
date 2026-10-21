package localauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ainsel-project/ainsel/services/hub/internal/authz"
)

// fakeGuardStore implements both TokenVersionReader (local JWT / chain
// middleware) and authz.DisabledUserChecker (the disabled guard) so one fake
// backs the full composed chain.
type fakeGuardStore struct {
	disabled map[string]bool
}

func (f fakeGuardStore) UserTokenVersion(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (f fakeGuardStore) IsUserDisabled(_ context.Context, id string) (bool, error) {
	if d, ok := f.disabled[id]; ok {
		return d, nil
	}
	return false, authz.ErrNotFound
}

// doChain sends a request carrying a freshly issued local session JWT for
// userID through the given composed handler chain.
func doChain(t *testing.T, handler http.Handler, userID string) *httptest.ResponseRecorder {
	t.Helper()
	secret := []byte("chain-test-secret-0123456789abcdef")
	tok, _, err := IssueToken(secret, userID, "alice", false, 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestDisabledGuard_InsideLocalChain pins the middleware nesting that makes
// the feature's headline claim true: disabling a user kills an *already
// issued, still-valid* local session JWT. This only holds because the guard
// sits inside NewMiddleware (which populates the user context) and inside
// RequireAuth. Moving the guard outside localMW still compiles and keeps
// every other test green — this test is what catches that re-ordering.
func TestDisabledGuard_InsideLocalChain(t *testing.T) {
	store := fakeGuardStore{disabled: map[string]bool{"local:alice": true, "local:bob": false}}
	secret := []byte("chain-test-secret-0123456789abcdef")
	chain := NewMiddleware(secret, store)(
		RequireAuth(authz.NewDisabledGuard(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))),
	)

	tok, _, err := IssueToken(secret, "local:alice", "alice", false, 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("valid JWT for disabled user: status = %d, want 401 (guard must sit inside localMW)", rec.Code)
	}
}

// TestDisabledGuard_InsideChainMiddleware pins the same nesting for the
// oidc+local coexistence chain (wireOIDCPlusLocalAuth): a valid local JWT for
// a disabled user must be rejected inside chainMW, before the handler runs.
func TestDisabledGuard_InsideChainMiddleware(t *testing.T) {
	store := fakeGuardStore{disabled: map[string]bool{"local:alice": true}}
	anonymousCloser := func(next http.Handler) http.Handler {
		return RequireAuth(next) // stands in for the OIDC middleware's rejection
	}
	secret := []byte("chain-test-secret-0123456789abcdef")
	chain := NewChainMiddleware(secret, store, anonymousCloser)(
		authz.NewDisabledGuard(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})),
	)

	tok, _, err := IssueToken(secret, "local:alice", "alice", false, 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/thing", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("valid JWT for disabled user via chainMW: status = %d, want 401", rec.Code)
	}
}

// TestDisabledGuard_EnabledUserPassesChain is the control: the same chain
// must let an enabled user through, so the 401 above is the guard, not a
// misassembled test chain.
func TestDisabledGuard_EnabledUserPassesChain(t *testing.T) {
	store := fakeGuardStore{disabled: map[string]bool{"local:alice": true, "local:bob": false}}
	secret := []byte("chain-test-secret-0123456789abcdef")
	mw := NewMiddleware(secret, store)(
		RequireAuth(authz.NewDisabledGuard(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))),
	)
	if rec := doChain(t, mw, "local:bob"); rec.Code != http.StatusOK {
		t.Fatalf("enabled user: status = %d, want 200", rec.Code)
	}
}
