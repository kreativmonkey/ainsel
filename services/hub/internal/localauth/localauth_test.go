package localauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ainsel-project/ainsel/services/hub/internal/authz"
	"github.com/golang-jwt/jwt/v5"

	"github.com/ainsel-project/ainsel/shared/auth/oidc"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if err := VerifyPassword("s3cret-password", hash); err != nil {
		t.Fatalf("VerifyPassword(correct): %v", err)
	}
	if err := VerifyPassword("wrong", hash); err == nil {
		t.Fatal("VerifyPassword(wrong) must fail")
	}
	if err := VerifyPassword("", hash); err == nil {
		t.Fatal("VerifyPassword(empty) must fail")
	}
}

func TestVerifyPasswordNoCredentials(t *testing.T) {
	if err := VerifyPassword("anything", ""); err == nil {
		t.Fatal("empty stored hash must fail")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, bad := range []string{
		"not-a-hash",
		"$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$garbage$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$!!!",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$",
	} {
		if err := VerifyPassword("x", bad); err == nil {
			t.Fatalf("VerifyPassword(%q) must fail", bad)
		}
	}
}

func TestVerifyPasswordDifferentParams(t *testing.T) {
	// Hashes are self-describing: verification must honor the stored params,
	// so a hash produced with different cost settings still verifies.
	hash, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the stored cost up; verification should recompute with the
	// claimed params and fail the comparison only if params were ignored —
	// here we just assert parsing different-but-valid params works by
	// rebuilding a hash with non-default params manually.
	alt := "$argon2id$v=19$m=1024,t=1,p=1$" +
		strings.Split(hash, "$")[4] + "$" + strings.Split(hash, "$")[5]
	// Recomputing with smaller memory yields a different key, so this must
	// NOT verify — proving params are actually honored.
	if err := VerifyPassword("pw", alt); err == nil {
		t.Fatal("verification must honor stored params (altered params should fail)")
	}
}

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestIssueAndVerifyToken(t *testing.T) {
	tok, exp, err := IssueToken(testSecret, "local:admin", "admin", true, 7)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if time.Until(exp) < 11*time.Hour || time.Until(exp) > 13*time.Hour {
		t.Fatalf("unexpected expiry: %v", exp)
	}
	c, err := VerifyToken(testSecret, tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if c.Sub != "local:admin" || c.Username != "admin" || !c.Admin {
		t.Fatalf("unexpected claims: %+v", c)
	}
	if c.TokenVersion != 7 {
		t.Fatalf("TokenVersion = %d, want 7", c.TokenVersion)
	}
}

func TestVerifyTokenMissingVersionClaim(t *testing.T) {
	// Tokens minted before versioning existed carry no token_version claim;
	// they must verify as version 0 (the stored default) so an upgrade never
	// force-logs-out active sessions.
	claims := jwt.MapClaims{
		"iss":      Issuer,
		"sub":      "local:alice",
		"username": "alice",
		"admin":    false,
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	c, err := VerifyToken(testSecret, tok)
	if err != nil {
		t.Fatalf("VerifyToken(legacy token): %v", err)
	}
	if c.TokenVersion != 0 {
		t.Fatalf("TokenVersion = %d, want 0 for missing claim", c.TokenVersion)
	}
}

func TestVerifyTokenWrongSecret(t *testing.T) {
	tok, _, _ := IssueToken(testSecret, "local:admin", "admin", true, 0)
	if _, err := VerifyToken([]byte("another-secret-another-secret"), tok); err == nil {
		t.Fatal("VerifyToken with wrong secret must fail")
	}
}

func TestLooksLocal(t *testing.T) {
	tok, _, _ := IssueToken(testSecret, "local:admin", "admin", true, 0)
	if !LooksLocal(tok) {
		t.Fatal("own token must look local")
	}
	if LooksLocal("not-a-jwt") {
		t.Fatal("garbage must not look local")
	}
	if LooksLocal("ainsel_user-token") {
		t.Fatal("user token must not look local")
	}
}

func TestIssueTokenEmptySecret(t *testing.T) {
	if _, _, err := IssueToken(nil, "local:a", "a", false, 0); err == nil {
		t.Fatal("IssueToken with empty secret must fail")
	}
}

// fakeVersionReader is a TokenVersionReader test double.
type fakeVersionReader struct {
	versions map[string]int64
	calls    int
	err      error // when set, returned as-is (simulates a store outage)
}

func (f *fakeVersionReader) UserTokenVersion(_ context.Context, userID string) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	v, ok := f.versions[userID]
	if !ok {
		return 0, authz.ErrNotFound
	}
	return v, nil
}

func TestMiddleware(t *testing.T) {
	var seen *oidc.User
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := oidc.FromContext(r.Context())
		seen = u
		w.WriteHeader(http.StatusOK)
	})
	reader := &fakeVersionReader{versions: map[string]int64{"local:alice": 0}}
	mw := NewMiddleware(testSecret, reader)

	t.Run("valid local token sets user", func(t *testing.T) {
		seen = nil
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if seen == nil || seen.Sub != "local:alice" || seen.Username != "alice" {
			t.Fatalf("user not set correctly: %+v", seen)
		}
	})

	t.Run("tampered local token gets 401 and does not fall through", func(t *testing.T) {
		seen = nil
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok+"x")
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if seen != nil {
			t.Fatal("inner handler must not run for invalid local token")
		}
	})

	t.Run("foreign token falls through", func(t *testing.T) {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer some.rs256.token")
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen != nil {
			t.Fatalf("expected fall-through without user, got status=%d user=%v", rec.Code, seen)
		}
	})

	t.Run("no token falls through", func(t *testing.T) {
		seen = nil
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen != nil {
			t.Fatalf("expected fall-through, got status=%d user=%v", rec.Code, seen)
		}
	})

	t.Run("stale token version gets 401", func(t *testing.T) {
		seen = nil
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 3)
		reader.versions["local:alice"] = 4 // password changed after mint
		defer func() { reader.versions["local:alice"] = 0 }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if seen != nil {
			t.Fatal("inner handler must not run for stale token version")
		}
	})

	t.Run("deleted user gets 401", func(t *testing.T) {
		seen = nil
		tok, _, _ := IssueToken(testSecret, "local:gone", "gone", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if seen != nil {
			t.Fatal("inner handler must not run for deleted user")
		}
	})

	t.Run("store error fails closed with 503", func(t *testing.T) {
		seen = nil
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		reader.err = errors.New("db down")
		defer func() { reader.err = nil }()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if seen != nil {
			t.Fatal("inner handler must not run when the version store fails")
		}
	})

	t.Run("foreign token does not hit the version store", func(t *testing.T) {
		seen = nil
		before := reader.calls
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer some.rs256.token")
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || seen != nil {
			t.Fatalf("expected fall-through, got status=%d user=%v", rec.Code, seen)
		}
		if reader.calls != before {
			t.Fatalf("version store must not be consulted for foreign tokens (calls: %d -> %d)", before, reader.calls)
		}
	})
}

// TestChainMiddleware covers the oidc+local coexistence combinator
// (issue #114). The fallback stands in for the OIDC middleware.
func TestChainMiddleware(t *testing.T) {
	var seen *oidc.User
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := oidc.FromContext(r.Context())
		seen = u
		w.WriteHeader(http.StatusOK)
	})
	fallbackCalled := false
	fallback := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fallbackCalled = true
			// Stand in for the OIDC middleware: anonymous → 401.
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "missing bearer token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	reader := &fakeVersionReader{versions: map[string]int64{"local:alice": 0}}
	mw := NewChainMiddleware(testSecret, reader, fallback)

	t.Run("valid local token authenticates without touching the fallback", func(t *testing.T) {
		seen, fallbackCalled = nil, false
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", true, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if fallbackCalled {
			t.Fatal("OIDC fallback must not run for a valid local token")
		}
		if seen == nil || seen.Sub != "local:alice" || seen.Username != "alice" {
			t.Fatalf("user not set correctly: %+v", seen)
		}
	})

	t.Run("stale token version gets 401 and never reaches the fallback", func(t *testing.T) {
		// Password change / reset / sign-out-everywhere bumps the stored
		// version; the session minted before it must die in coexistence mode
		// too, not fall through to the OIDC middleware.
		seen, fallbackCalled = nil, false
		reader.versions["local:alice"] = 1
		defer func() { reader.versions["local:alice"] = 0 }()
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if fallbackCalled || seen != nil {
			t.Fatalf("stale local token must not fall through: fallback=%v user=%v", fallbackCalled, seen)
		}
	})

	t.Run("version store outage gets 503 and never reaches the fallback", func(t *testing.T) {
		seen, fallbackCalled = nil, false
		reader.err = errors.New("db down")
		defer func() { reader.err = nil }()
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if fallbackCalled || seen != nil {
			t.Fatalf("must fail closed: fallback=%v user=%v", fallbackCalled, seen)
		}
	})

	t.Run("tampered local token gets 401 and never reaches the fallback", func(t *testing.T) {
		seen, fallbackCalled = nil, false
		tok, _, _ := IssueToken(testSecret, "local:alice", "alice", false, 0)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+tok+"x")
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		if fallbackCalled || seen != nil {
			t.Fatalf("local-fail must not fall through: fallback=%v user=%v", fallbackCalled, seen)
		}
	})

	t.Run("foreign token is delegated to the fallback", func(t *testing.T) {
		seen, fallbackCalled = nil, false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer some.rs256.token")
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !fallbackCalled || seen != nil {
			t.Fatalf("expected delegation, got status=%d fallback=%v user=%v", rec.Code, fallbackCalled, seen)
		}
	})

	t.Run("anonymous request is delegated and rejected by the fallback", func(t *testing.T) {
		seen, fallbackCalled = nil, false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		mw(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !fallbackCalled {
			t.Fatalf("expected 401 from fallback, got status=%d fallback=%v", rec.Code, fallbackCalled)
		}
		if body := rec.Body.String(); !strings.Contains(body, "missing bearer token") {
			t.Fatalf("anonymous rejection must come from the fallback: %q", body)
		}
	})
}

func TestRequireAuth(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	t.Run("no user rejected", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		RequireAuth(inner).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || reached {
			t.Fatalf("expected 401 without reaching handler, got status=%d reached=%v", rec.Code, reached)
		}
	})

	t.Run("authenticated user passes", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := oidc.ContextWithUser(req.Context(), &oidc.User{Sub: "local:alice", Username: "alice"})
		rec := httptest.NewRecorder()
		RequireAuth(inner).ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != http.StatusOK || !reached {
			t.Fatalf("expected 200 and handler reached, got status=%d reached=%v", rec.Code, reached)
		}
	})
}
