package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
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

// standardClaims builds a valid claim set for this issuer and audience,
// representing a properly-configured IdP that asserts a verified email.
func (fi *fakeIssuer) standardClaims(aud string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":            fi.url,
		"aud":            aud,
		"sub":            "user-123",
		"email":          "ada@example.com",
		"email_verified": true,
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
}

const testAudience = "api://gws-mcp"

func TestVerifierAcceptsValidToken(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
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
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "preferred_username", true)

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

// TestVerifierRequiresEmailVerified is the impersonation-safety guard: when the
// subject claim is "email" and verification is required, a token whose
// email_verified is missing or false is rejected — so an unverified/mutable
// email can never become a DWD impersonation target.
func TestVerifierRequiresEmailVerified(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)

	t.Run("missing email_verified rejected", func(t *testing.T) {
		claims := fi.standardClaims(testAudience)
		delete(claims, "email_verified")
		if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
			t.Fatal("expected rejection when email_verified is absent")
		}
	})

	t.Run("email_verified false rejected", func(t *testing.T) {
		claims := fi.standardClaims(testAudience)
		claims["email_verified"] = false
		if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
			t.Fatal("expected rejection when email_verified is false")
		}
	})

	t.Run("string true accepted", func(t *testing.T) {
		claims := fi.standardClaims(testAudience)
		claims["email_verified"] = "true" // some IdPs stringify the boolean
		if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err != nil {
			t.Fatalf("expected acceptance for email_verified=\"true\": %v", err)
		}
	})
}

// TestVerifierTrustUnverifiedEmailOptOut proves the escape hatch: with the check
// disabled, an unverified email is accepted (the operator has vouched for the
// issuer).
func TestVerifierTrustUnverifiedEmailOptOut(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", false)
	claims := fi.standardClaims(testAudience)
	delete(claims, "email_verified")
	got, err := v.Verify(context.Background(), fi.sign(t, claims))
	if err != nil {
		t.Fatalf("expected acceptance with the check disabled: %v", err)
	}
	if got.GoogleUser != "ada@example.com" {
		t.Errorf("GoogleUser = %q", got.GoogleUser)
	}
}

// TestVerifierEmailVerifiedIgnoredForNonEmailClaim confirms the check only
// applies when the mapping claim is "email": a custom claim the operator chose is
// not subject to email_verified.
func TestVerifierEmailVerifiedIgnoredForNonEmailClaim(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "preferred_username", true)
	claims := fi.standardClaims(testAudience)
	delete(claims, "email_verified")
	claims["preferred_username"] = "grace@example.com"
	if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err != nil {
		t.Fatalf("email_verified must not gate a non-email subject claim: %v", err)
	}
}

func TestVerifierRejectsWrongAudience(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	_, err := v.Verify(context.Background(), fi.sign(t, fi.standardClaims("some-other-api")))
	if err == nil {
		t.Fatal("expected rejection for wrong audience")
	}
}

func TestVerifierRejectsExpired(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	claims := fi.standardClaims(testAudience)
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
		t.Fatal("expected rejection for expired token")
	}
}

func TestVerifierRejectsWrongIssuer(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	claims := fi.standardClaims(testAudience)
	claims["iss"] = "https://attacker.example.com"
	if _, err := v.Verify(context.Background(), fi.sign(t, claims)); err == nil {
		t.Fatal("expected rejection for issuer mismatch")
	}
}

func TestVerifierEmptyWhenClaimAbsent(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
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
	if _, err := NewVerifier(context.Background(), []string{"https://x"}, "", "email", true); err == nil {
		t.Error("expected error with empty audience")
	}
	if _, err := NewVerifier(context.Background(), nil, testAudience, "email", true); err == nil {
		t.Error("expected error with no issuers")
	}
}

func TestVerifierRejectsMalformedToken(t *testing.T) {
	fi := newFakeIssuer(t)
	v, _ := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	if _, err := v.Verify(context.Background(), "not.a.jwt.at.all"); err == nil {
		t.Fatal("expected rejection for malformed token")
	}
	// Sanity: issuerOf on a non-3-part token errors without panicking.
	if _, err := issuerOf("abc"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("issuerOf malformed = %v", err)
	}
}

// TestVerifierRejectsForeignSignature is the test the suite was missing. Every
// other rejection case feeds a token signed by the TRUSTED key and varies a
// claim, so all of them would still pass if Verify stopped checking signatures
// and just parsed the payload. This one signs with a key the issuer's JWKS does
// not publish: only real signature verification rejects it.
//
// It is the difference between "the verifier reads claims correctly" and "the
// verifier cannot be handed a token an attacker minted".
func TestVerifierRejectsForeignSignature(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	// An attacker's key, never published in the issuer's JWKS.
	attacker, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate attacker key: %v", err)
	}

	for _, tc := range []struct {
		name string
		kid  string
	}{
		// Claiming the issuer's kid is the interesting case: the verifier finds a
		// key by that id and must still reject the mismatched signature.
		{name: "attacker key claiming the issuer's kid", kid: fi.kid},
		{name: "attacker key with an unknown kid", kid: "attacker-key"},
	} {
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: attacker},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", tc.kid),
		)
		if err != nil {
			t.Fatalf("%s: new signer: %v", tc.name, err)
		}
		// Claims are otherwise perfect: right issuer, audience, subject, expiry.
		payload, err := json.Marshal(fi.standardClaims(testAudience))
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		jws, err := signer.Sign(payload)
		if err != nil {
			t.Fatalf("%s: sign: %v", tc.name, err)
		}
		tok, err := jws.CompactSerialize()
		if err != nil {
			t.Fatalf("%s: serialize: %v", tc.name, err)
		}

		if _, err := v.Verify(context.Background(), tok); err == nil {
			t.Errorf("%s: token with a forged signature was ACCEPTED", tc.name)
		}
	}
}

// A token whose payload has been edited after signing must be rejected: the
// signature no longer covers the claims.
func TestVerifierRejectsTamperedPayload(t *testing.T) {
	fi := newFakeIssuer(t)
	v, err := NewVerifier(context.Background(), []string{fi.url}, testAudience, "email", true)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	tok := fi.sign(t, fi.standardClaims(testAudience))
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("baseline token should verify: %v", err)
	}

	// Swap the payload for one naming a different user, keeping the signature.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape")
	}
	claims := fi.standardClaims(testAudience)
	claims["email"] = "admin@example.com"
	claims["sub"] = "someone-else"
	edited, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(edited) + "." + parts[2]

	if _, err := v.Verify(context.Background(), forged); err == nil {
		t.Error("token with an edited payload was ACCEPTED — claims are not signature-protected")
	}
}
