package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ainsel-project/ainsel/services/mcp/internal/auth"
	"github.com/ainsel-project/ainsel/shared/auth/oidc"
	"github.com/golang-jwt/jwt/v5"
)

func TestNewMiddlewareRejectsMissingToken_AddsWWWAuthenticate(t *testing.T) {
	// The middleware no longer derives the JWKS URL from the issuer; the
	// test passes the resolved endpoint explicitly.
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(jwksServer.Close)

	mw, err := auth.NewMiddleware(auth.Config{
		Issuer:   jwksServer.URL,
		Audience: "proj-123",
		JWKSURL:  jwksServer.URL + "/keys",
	})
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called on missing token")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", rec.Code)
	}
	got := rec.Header().Get("WWW-Authenticate")
	if got != `Bearer error="invalid_token"` {
		t.Errorf("WWW-Authenticate = %q; want Bearer error=\"invalid_token\"", got)
	}
	if !strings.Contains(rec.Body.String(), "missing bearer token") {
		t.Errorf("body = %q; want contains 'missing bearer token'", rec.Body.String())
	}
}

func TestNewMiddlewareConfiguredSigningAlgorithm(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "ed25519",
			"x": base64.RawURLEncoding.EncodeToString(publicKey),
		}}})
	}))
	t.Cleanup(jwksServer.Close)

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": jwksServer.URL, "aud": "ainsel", "sub": "alice",
		"exp": time.Now().Add(time.Minute).Unix(),
	})
	token.Header["kid"] = "ed25519"
	raw, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		algorithms []string
		status     int
	}{
		{"explicit EdDSA", []string{"EdDSA"}, http.StatusOK},
		{"default RS256 rejects EdDSA", nil, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mw, err := auth.NewMiddleware(auth.Config{
				Issuer: jwksServer.URL, Audience: "ainsel", JWKSURL: jwksServer.URL,
				SigningAlgs: tc.algorithms,
			})
			if err != nil {
				t.Fatal(err)
			}
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u, ok := oidc.FromContext(r.Context())
				if !ok || u.Sub != "alice" {
					t.Errorf("authenticated identity = %v; want alice", u)
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer "+raw)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Errorf("status = %d; want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestNewMiddlewareRejectsEmptyJWKSURL(t *testing.T) {
	// No legacy fallback: an empty JWKSURL must error instead of silently
	// constructing the legacy Zitadel path from the issuer.
	_, err := auth.NewMiddleware(auth.Config{
		Issuer:   "https://idp.example.com",
		Audience: "proj-123",
	})
	if err == nil {
		t.Fatal("expected error for empty JWKSURL")
	}
	if !strings.Contains(err.Error(), "JWKSURL is required") {
		t.Errorf("err = %v; want 'JWKSURL is required'", err)
	}
}
