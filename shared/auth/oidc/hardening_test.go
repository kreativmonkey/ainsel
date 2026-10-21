package oidc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ainsel-project/ainsel/shared/auth/oidc"
	"github.com/golang-jwt/jwt/v5"
)

func oidcTestEnvironment(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OIDC_ISSUER", "ZITADEL_ISSUER", "OIDC_AUDIENCE", "OIDC_PROJECT_ID", "OIDC_CLIENT_ID", "OIDC_ROLES_CLAIM", "OIDC_SIGNING_ALGS"} {
		t.Setenv(k, "")
	}
	t.Setenv("OIDC_ISSUER", "https://provider.test/application/o/ainsel/")
	t.Setenv("OIDC_CLIENT_ID", "ainsel")
	if err := os.Unsetenv("OIDC_SIGNING_ALGS"); err != nil {
		t.Fatal(err)
	}
	oidc.ClearUserInfoCache()
}
func oidcTestClaims() jwt.MapClaims {
	return jwt.MapClaims{"iss": "https://provider.test/application/o/ainsel/", "aud": "ainsel", "sub": "alice", "exp": time.Now().Add(time.Minute).Unix()}
}
func authenticateToken(t *testing.T, cfg oidc.Config, token string) (int, *oidc.User) {
	t.Helper()
	mw, err := oidc.NewMiddleware(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var user *oidc.User
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _ = oidc.FromContext(r.Context())
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, user
}
func TestMiddlewareSigningAlgorithms(t *testing.T) {
	for _, alg := range []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"} {
		t.Run(alg, func(t *testing.T) {
			oidcTestEnvironment(t)
			var key any
			method := jwt.GetSigningMethod(alg)
			var jwk map[string]any
			switch {
			case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
				rsaKey := newRSAKey(t)
				key = rsaKey
				jwk = map[string]any{"kty": "RSA", "n": base64.RawURLEncoding.EncodeToString(rsaKey.N.Bytes()), "e": "AQAB"}
			case strings.HasPrefix(alg, "ES"):
				curve := elliptic.P256()
				crv := "P-256"
				if alg == "ES384" {
					curve = elliptic.P384()
					crv = "P-384"
				}
				if alg == "ES512" {
					curve = elliptic.P521()
					crv = "P-521"
				}
				size := (curve.Params().BitSize + 7) / 8
				ec, err := ecdsa.GenerateKey(curve, rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				key = ec
				jwk = map[string]any{"kty": "EC", "crv": crv, "x": base64.RawURLEncoding.EncodeToString(ec.X.FillBytes(make([]byte, size))), "y": base64.RawURLEncoding.EncodeToString(ec.Y.FillBytes(make([]byte, size)))}
			case alg == "EdDSA":
				pub, priv, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				key = priv
				jwk = map[string]any{"kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(pub)}
			}
			jwk["alg"] = alg
			jwk["kid"] = "test-key"
			jwk["use"] = "sig"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{jwk}})
			}))
			defer server.Close()
			if alg != "RS256" {
				t.Setenv("OIDC_SIGNING_ALGS", alg)
			}
			cfg := oidc.FromEnvironment()
			cfg.JWKSURL = server.URL
			tok := jwt.NewWithClaims(method, oidcTestClaims())
			tok.Header["kid"] = "test-key"
			token, err := tok.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			status, _ := authenticateToken(t, cfg, token)
			if status != 200 {
				t.Fatalf("configured %s JWT: want 200, got %d", alg, status)
			}
		})
	}
}

func TestMiddlewareRejectsUnsafeSigningConfiguration(t *testing.T) {
	for _, algs := range [][]string{{}, {""}, {"HS256"}, {"none"}, {"unknown"}, {"RS256", ""}, {"RS256", "HS512"}} {
		t.Run(fmt.Sprint(algs), func(t *testing.T) {
			_, err := oidc.NewMiddleware(oidc.Config{Issuer: "https://provider.test", Audience: "app", JWKSURL: "http://127.0.0.1:1/keys", SigningAlgs: algs})
			if err == nil || !strings.Contains(err.Error(), "signing algorithm") {
				t.Fatalf("want signing configuration error before JWKS fetch, got %v", err)
			}
		})
	}
}
func TestMiddlewareNestedRoles(t *testing.T) {
	oidcTestEnvironment(t)
	t.Setenv("OIDC_ROLES_CLAIM", "realm_access.roles")
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	claims := oidcTestClaims()
	claims["realm_access"] = map[string]any{"roles": []string{"admin"}}
	status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if !reflect.DeepEqual(user.Roles, []string{"admin"}) {
		t.Fatalf("nested roles: want [admin], got %v", user.Roles)
	}
}
func TestMiddlewareDefaultRolesClaimIsGroups(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	claims := oidcTestClaims()
	claims["groups"] = []string{"viewer"}
	claims["roles"] = []string{"unrelated"}
	claims["urn:zitadel:iam:org:project:roles"] = map[string]any{"unrelated": map[string]any{}}
	status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if !reflect.DeepEqual(user.Roles, []string{"viewer"}) {
		t.Fatalf("default claim: want [viewer] from groups, got %v", user.Roles)
	}
}
func TestMiddlewareMapRolesAreSorted(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	cfg.RolesClaim = zitadelRolesClaim
	// Enough keys that Go's randomised map iteration practically never
	// yields sorted order by accident.
	roles := map[string]any{}
	var want []string
	for i := 19; i >= 0; i-- {
		role := fmt.Sprintf("role-%02d", i)
		roles[role] = map[string]any{}
		want = append([]string{role}, want...)
	}
	claims := oidcTestClaims()
	claims["urn:zitadel:iam:org:project:roles"] = roles
	status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if !reflect.DeepEqual(user.Roles, want) {
		t.Fatalf("map roles: want sorted %v, got %v", want, user.Roles)
	}
}
func TestMiddlewareUserInfoSubjectMismatch(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	userinfo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "bob", "preferred_username": "Bob"})
	}))
	defer userinfo.Close()
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	cfg.UserInfoURL = userinfo.URL
	status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", oidcTestClaims()))
	if status != 401 {
		t.Fatalf("userinfo sub mismatch: want 401, got %d; authenticated user = %+v", status, user)
	}
}

func TestMiddlewareRequiresSubject(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	for _, subject := range []any{nil, "", 123, []string{"alice"}} {
		t.Run(fmt.Sprint(subject), func(t *testing.T) {
			claims := oidcTestClaims()
			claims["sub"] = subject
			status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
			if status != http.StatusUnauthorized || user != nil {
				t.Fatalf("invalid subject: status %d, user %+v", status, user)
			}
		})
	}
	claims := oidcTestClaims()
	delete(claims, "sub")
	status, _ := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
	if status != http.StatusUnauthorized {
		t.Fatalf("absent subject: status %d", status)
	}
}

func TestMiddlewareRoleClaimShapes(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	for _, tc := range []struct {
		name, path string
		claims     jwt.MapClaims
		want       []string
	}{
		{"array", "groups", jwt.MapClaims{"groups": []string{"viewer"}}, []string{"viewer"}},
		{"map", "groups", jwt.MapClaims{"groups": map[string]any{"viewer": map[string]any{}}}, []string{"viewer"}},
		{"nested map", "realm_access.roles", jwt.MapClaims{"realm_access": map[string]any{"roles": map[string]any{"viewer": true}}}, []string{"viewer"}},
		{"literal dotted key", "realm_access.roles", jwt.MapClaims{"realm_access.roles": []string{"viewer"}, "realm_access": map[string]any{"roles": []string{"admin"}}}, []string{"viewer"}},
		{"missing", "realm_access.roles", jwt.MapClaims{}, nil},
		{"invalid nesting", "realm_access.roles", jwt.MapClaims{"realm_access": "invalid"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := oidc.FromEnvironment()
			cfg.JWKSURL = server.URL
			cfg.RolesClaim = tc.path
			claims := oidcTestClaims()
			for k, v := range tc.claims {
				claims[k] = v
			}
			status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
			if status != http.StatusOK {
				t.Fatalf("status %d", status)
			}
			if !reflect.DeepEqual(user.Roles, tc.want) {
				t.Fatalf("roles %v, want %v", user.Roles, tc.want)
			}
		})
	}
}

func TestMiddlewareRejectsInvalidSignature(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	status, user := authenticateToken(t, cfg, signedToken(t, newRSAKey(t), "test-key", oidcTestClaims()))
	if status != http.StatusUnauthorized || user != nil {
		t.Fatalf("invalid signature accepted: status %d, user %+v", status, user)
	}
}

func TestFetchUserInfoRequiresMatchingSubject(t *testing.T) {
	for _, subject := range []any{nil, "", "bob", 123} {
		t.Run(fmt.Sprint(subject), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"sub": subject, "email": "wrong@example.com"})
			}))
			defer server.Close()
			info, err := oidc.FetchUserInfo(context.Background(), server.Client(), server.URL, "access-token", "alice")
			if !errors.Is(err, oidc.ErrUserInfoSubjectMismatch) || info != nil {
				t.Fatalf("mismatching userinfo: result %+v, error %v", info, err)
			}
		})
	}
}

func TestMiddlewareRejectsInvalidClaims(t *testing.T) {
	oidcTestEnvironment(t)
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	for _, tc := range []struct {
		name, claim string
		value       any
	}{
		{"wrong issuer", "iss", "https://other.test"},
		{"missing issuer", "iss", nil},
		{"missing expiry", "exp", nil},
		{"not yet valid", "nbf", time.Now().Add(time.Hour).Unix()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := oidcTestClaims()
			if tc.value == nil {
				delete(claims, tc.claim)
			} else {
				claims[tc.claim] = tc.value
			}
			status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
			if status != http.StatusUnauthorized || user != nil {
				t.Fatalf("invalid claims accepted: status %d, user %+v", status, user)
			}
		})
	}
}
func TestMiddlewareLegacyZitadelEnvironment(t *testing.T) {
	oidcTestEnvironment(t)
	t.Setenv("OIDC_ISSUER", "")
	t.Setenv("ZITADEL_ISSUER", "https://zitadel.test")
	t.Setenv("OIDC_PROJECT_ID", "project-123")
	t.Setenv("OIDC_CLIENT_ID", "browser-client-456")
	key := newRSAKey(t)
	server := startJWKSServer(t, key, "test-key")
	cfg := oidc.FromEnvironment()
	cfg.JWKSURL = server.URL
	claims := oidcTestClaims()
	claims["iss"] = "https://zitadel.test"
	claims["aud"] = []string{"project-123", "browser-client-456"}
	claims["urn:zitadel:iam:org:project:roles"] = map[string]any{"viewer": map[string]any{}}
	status, user := authenticateToken(t, cfg, signedToken(t, key, "test-key", claims))
	if status != 200 || user == nil || !reflect.DeepEqual(user.Roles, []string{"viewer"}) {
		t.Fatalf("legacy Zitadel: status %d; user %+v", status, user)
	}
}
