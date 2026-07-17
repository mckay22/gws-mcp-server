// Package oidcauth verifies incoming OAuth bearer tokens for resource-server
// mode. It is issuer-agnostic: any OIDC-compliant issuer (Keycloak, Microsoft
// Entra ID, Google itself, …) works, selected by an issuer allowlist. Signature
// (JWKS), issuer, audience, and expiry validation is delegated to
// github.com/coreos/go-oidc rather than hand-rolled; the package exposes only the
// claims needed to map a caller to a Google identity. Token values are never
// logged.
//
// This generalizes entra-mcp-server's tenant-specific verifier: instead of
// building issuers from Entra tenant ids, it trusts an explicit list of issuer
// URLs and maps the caller through a configurable claim (default "email").
package oidcauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims are the verified token fields used to identify the caller.
type Claims struct {
	// Subject is the token `sub` — a stable per-issuer principal id.
	Subject string
	// GoogleUser is the value of the configured subject claim (default "email"),
	// used as the DWD impersonation target. It may be empty if the token omits
	// the claim, which the identity backend treats as an unmappable caller.
	GoogleUser string
	// Expiry is the token expiry (`exp`).
	Expiry time.Time
}

// emailClaim is the standard OIDC claim whose trustworthiness is governed by the
// companion email_verified claim.
const emailClaim = "email"

// Verifier validates bearer tokens against one or more trusted OIDC issuers for
// a single audience, extracting a configurable Google-user claim. It is safe for
// concurrent use.
type Verifier struct {
	verifiers    map[string]*oidc.IDTokenVerifier // keyed by trusted issuer URL
	subjectClaim string
	// requireEmailVerified, when true and subjectClaim is "email", rejects a token
	// whose email_verified claim is not true — so an unverified/mutable email can
	// never become a DWD impersonation target.
	requireEmailVerified bool
}

// NewVerifier trusts the given issuer URLs and requires audience (this server's
// identifier). subjectClaim names the claim mapped to a Google user (e.g.
// "email"). When requireEmailVerified is true and subjectClaim is "email", a
// token must additionally carry email_verified==true to be accepted. It discovers
// each issuer's OIDC metadata — and thus its JWKS — so it performs network I/O.
func NewVerifier(ctx context.Context, issuers []string, audience, subjectClaim string, requireEmailVerified bool) (*Verifier, error) {
	if strings.TrimSpace(audience) == "" {
		return nil, errors.New("oidcauth: audience is required")
	}
	if strings.TrimSpace(subjectClaim) == "" {
		return nil, errors.New("oidcauth: subject claim is required")
	}
	verifiers := make(map[string]*oidc.IDTokenVerifier, len(issuers))
	for _, iss := range issuers {
		iss = strings.TrimSpace(iss)
		if iss == "" {
			continue
		}
		provider, err := oidc.NewProvider(ctx, iss)
		if err != nil {
			return nil, fmt.Errorf("oidcauth: discover issuer %q: %w", iss, err)
		}
		verifiers[iss] = provider.Verifier(&oidc.Config{ClientID: audience})
	}
	if len(verifiers) == 0 {
		return nil, errors.New("oidcauth: at least one issuer is required")
	}
	return &Verifier{verifiers: verifiers, subjectClaim: subjectClaim, requireEmailVerified: requireEmailVerified}, nil
}

// Verify validates the token — signature against the selected issuer's JWKS,
// issuer (one of the trusted set), audience, and expiry — and returns the
// caller's claims. A non-nil error means the token is not to be trusted; the
// error never contains the token.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	verifier, err := v.verifierFor(rawToken)
	if err != nil {
		return Claims{}, err
	}
	tok, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("oidcauth: verify token: %w", err)
	}

	// Pull the full claim set so the configured subject claim can be read
	// dynamically (it varies by IdP).
	var all map[string]any
	if err := tok.Claims(&all); err != nil {
		return Claims{}, fmt.Errorf("oidcauth: decode token claims: %w", err)
	}
	googleUser, _ := all[v.subjectClaim].(string)
	googleUser = strings.TrimSpace(googleUser)

	// When impersonation binds to the email claim, that email must be verified —
	// otherwise a token carrying an unverified/mutable email would let the caller
	// be impersonated as an arbitrary Workspace user. Only enforced when a
	// non-empty email was actually extracted; an absent claim yields an unmappable
	// caller that the resource-server layer rejects anyway.
	if v.requireEmailVerified && v.subjectClaim == emailClaim && googleUser != "" {
		if !claimIsTrue(all["email_verified"]) {
			return Claims{}, errors.New("oidcauth: email claim is not verified (email_verified != true); " +
				"impersonation refused — set GWS_TRUST_UNVERIFIED_EMAIL=true only if every trusted issuer asserts verified emails")
		}
	}

	return Claims{
		Subject:    tok.Subject,
		GoogleUser: googleUser,
		Expiry:     tok.Expiry,
	}, nil
}

// claimIsTrue reports whether an OIDC boolean-ish claim is true. Issuers encode
// booleans as either a JSON bool or the string "true", so both are accepted.
func claimIsTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}

// verifierFor selects the OIDC verifier for the token. With a single trusted
// issuer it is used directly; with several, the token's (still unverified) `iss`
// claim routes to the matching verifier, which then re-checks the issuer
// cryptographically — so peeking here cannot bypass validation. An untrusted
// issuer is rejected with a clear error.
func (v *Verifier) verifierFor(rawToken string) (*oidc.IDTokenVerifier, error) {
	if len(v.verifiers) == 1 {
		for _, verifier := range v.verifiers {
			return verifier, nil
		}
	}
	iss, err := issuerOf(rawToken)
	if err != nil {
		return nil, err
	}
	verifier, ok := v.verifiers[iss]
	if !ok {
		return nil, fmt.Errorf("oidcauth: untrusted issuer %q", iss)
	}
	return verifier, nil
}

// issuerOf reads the `iss` claim from a JWT without verifying its signature. It
// is used only to route a token to the right verifier; that verifier then
// re-validates the issuer, so an attacker cannot gain trust by spoofing `iss`.
// Errors never include the token itself.
func issuerOf(rawToken string) (string, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return "", errors.New("oidcauth: malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("oidcauth: malformed token payload")
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("oidcauth: malformed token payload")
	}
	return claims.Issuer, nil
}
