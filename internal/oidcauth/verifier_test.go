package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// fakeIssuer stands up an OIDC issuer: OIDC discovery, a JWKS with one RSA key,
// and a helper to sign JWTs with that key.
type fakeIssuer struct {
	url    string
	priv   *rsa.PrivateKey
	kid    string
	signer jose.Signer
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	fi := &fakeIssuer{priv: priv, kid: "test-key-1"}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", fi.kid),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	fi.signer = signer

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                fi.url,
			"jwks_uri":                              fi.url + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       priv.Public(),
			KeyID:     fi.kid,
			Algorithm: "RS256",
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fi.url = srv.URL
	return fi
}

// sign builds a signed JWT from the given claims.
func (fi *fakeIssuer) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	jws, err := fi.signer.Sign(payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok, err := jws.CompactSerialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return tok
}

// standardClaims builds a valid claim set for this issuer and audience.
func (fi *fakeIssuer) standardClaims(aud string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":   fi.url,
		"aud":   aud,
		"sub":   "user-123",
		"email": "ada@example.com",
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

const testAudience = "api://gws-mcp"

func TestVerifierAcceptsValidToken(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	claims, err := v.Verify(context.Background(), fi.sign(t, fi.standardClaims(testAudience)))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("Subject = %q", claims.Subject)
	}
	if claims.GoogleUser != "ada@example.com" {
		t.Errorf("GoogleUser = %q, want ada@example.com", claims.GoogleUser)
	}
}

func TestVerifierMapsConfigurableClaim(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "preferred_username")

	claims := fi.standardClaims(testAudience)
	claims["preferred_username"] = "grace@example.com"
	got, err := v.Verify(context.Background(), fi.sign(t, claims))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.GoogleUser != "grace@example.com" {
		t.Errorf("GoogleUser = %q, want the preferred_username claim", got.GoogleUser)
	}
}

func TestVerifierRejectsWrongAudience(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	_, err := v.Verify(context.Background(), fi.sign(t, fi.standardClaims("some-other-api")))
	if err == nil {
		t.Fatal("expected rejection for wrong audience")
	}
}

func TestVerifierRejectsExpired(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	claims := fi.standardClaims(testAudience)
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
		t.Fatal("expected rejection for expired token")
	}
}

func TestVerifierRejectsWrongIssuer(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	claims := fi.standardClaims(testAudience)
	claims["iss"] = "https://attacker.example.com"
	if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
		t.Fatal("expected rejection for issuer mismatch")
	}
}

func TestVerifierEmptyWhenClaimAbsent(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	claims := fi.standardClaims(testAudience)
	delete(claims, "email")
	got, err := v.Verify(context.Background(), fi.sign(t, claims))
	if err != nil {
		t.Fatalf("Verify: %v", err) // a valid token with no email is still valid...
	}
	if got.GoogleUser != "" {
		t.Errorf("GoogleUser = %q, want empty when claim absent", got.GoogleUser) // ...but unmappable
	}
}

func TestNewVerifierValidatesArgs(t *testing.T) {
	if _, err := NewVerifier(context.Background(), []string{"https://x"}, "", "email"); err == nil {
		t.Error("expected error with empty audience")
	}
	if _, err := NewVerifier(context.Background(), nil, testAudience, "email"); err == nil {
		t.Error("expected error with no issuers")
	}
}

func TestVerifierRejectsMalformedToken(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email")
	if _, err := v.Verify(context.Background(), "not.a.jwt.at.all"); err == nil {
		t.Fatal("expected rejection for malformed token")
	}
	// Sanity: issuerOf on a non-3-part token errors without panicking.
	if _, err := issuerOf("abc"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("issuerOf malformed = %v", err)
	}
}
