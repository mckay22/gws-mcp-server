package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// This file implements the resource-server DWD (domain-wide delegation)
// credential provider — the Google analog of entra-mcp-server's On-Behalf-Of
// backend. There is no single signed-in user: each request carries a validated
// bearer token that the verifier maps to a Google user, and this provider mints a
// service-account-signed JWT with sub=<that user> (domain-wide delegation),
// exchanging it for an access token so the API is called as that user. Google
// still enforces the impersonated user's own authorization.
//
// Secrets discipline: neither the service-account key nor any minted token is
// ever printed, logged, or formatted into an error.

// userCtxKey is the unexported context key under which the bearer middleware
// stashes the verified caller's Google user (the DWD impersonation target). A
// private zero-size type prevents collisions with other packages' ctx values.
type userCtxKey struct{}

// WithUser returns a copy of ctx carrying the Google user the DWD backend should
// impersonate. The bearer middleware calls this after it validates the incoming
// token and maps it to a Google identity.
func WithUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, userCtxKey{}, user)
}

// UserFromContext returns the impersonation target previously attached by
// WithUser. The boolean is false when none is present.
func UserFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(userCtxKey{}).(string)
	return v, ok
}

// errNoUser is returned by GoogleToken when the context carries no impersonation
// target. In resource-server mode the bearer middleware must have validated a
// token and called WithUser first; its absence is a server wiring bug, not a user
// error.
var errNoUser = errors.New("dwd: no impersonation target on request context")

// DWD implements the gapi TokenSource interface
//
//	GoogleToken(ctx context.Context) (string, error)
//
// for resource-server mode. For each distinct impersonated user it builds a
// domain-wide-delegated, refreshing oauth2 token source (keyed by user email) and
// caches it. Many users share one process, so the cache is a map; it is safe for
// concurrent use.
type DWD struct {
	// newSource builds a refreshing token source that impersonates subject via
	// DWD. It is a field so tests can substitute a fake without a real SA key.
	newSource func(ctx context.Context, subject string) (oauth2.TokenSource, error)

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource // key: user email
}

// Compile-time assertion that DWD satisfies the gapi TokenSource contract.
var _ interface {
	GoogleToken(ctx context.Context) (string, error)
} = (*DWD)(nil)

// NewDWD builds the resource-server DWD credential provider. It reads the
// service-account JSON key from keyPath (a domain credential — never logged) and
// prepares to mint tokens for the given scopes with the impersonated user as the
// JWT subject. It performs no exchange: the first mint for a given user happens
// lazily on that user's first GoogleToken call.
func NewDWD(keyPath string, scopes []string) (*DWD, error) {
	keyJSON, err := os.ReadFile(keyPath)
	if err != nil {
		// The path is not secret; the key contents (not included here) are.
		return nil, fmt.Errorf("reading DWD service-account key: %w", err)
	}
	// Validate the key up front so a broken key fails at startup, not per request.
	if _, err := google.JWTConfigFromJSON(keyJSON, scopes...); err != nil {
		return nil, fmt.Errorf("parsing DWD service-account key: %w", err)
	}
	return &DWD{
		newSource: func(ctx context.Context, subject string) (oauth2.TokenSource, error) {
			cfg, err := google.JWTConfigFromJSON(keyJSON, scopes...)
			if err != nil {
				return nil, fmt.Errorf("building DWD JWT config: %w", err)
			}
			cfg.Subject = subject // domain-wide delegation: act as this user
			return cfg.TokenSource(ctx), nil
		},
		sources: make(map[string]oauth2.TokenSource),
	}, nil
}

// KeyIdentity returns a stable, non-secret identifier for a service-account key
// file: its client_email and private_key_id. Neither is a secret — the private
// key material is never read out — so the result is safe to compare and to name
// in an error.
//
// It exists so the tiers can prove they hold *different* service accounts: the
// path check in config catches aliases of one file, but a copied key is a
// distinct file carrying the same credential, and only its identity reveals that.
func KeyIdentity(keyPath string) (clientEmail, privateKeyID string, err error) {
	keyJSON, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("reading service-account key: %w", err)
	}
	var key struct {
		ClientEmail  string `json:"client_email"`
		PrivateKeyID string `json:"private_key_id"`
	}
	if err := json.Unmarshal(keyJSON, &key); err != nil {
		// The message deliberately omits the content, which holds the private key.
		return "", "", errors.New("parsing service-account key: not valid JSON")
	}
	return key.ClientEmail, key.PrivateKeyID, nil
}

// GoogleToken returns a bearer token for calling the Google APIs as the user on
// ctx (attached by WithUser). It serves/refreshes a cached per-user token source,
// minting a DWD-signed assertion for that user on first use. Distinct users cache
// independently. It is goroutine-safe. The returned token is never logged.
func (d *DWD) GoogleToken(ctx context.Context) (string, error) {
	user, ok := UserFromContext(ctx)
	if !ok || user == "" {
		return "", errNoUser
	}

	d.mu.Lock()
	src := d.sources[user]
	if src == nil {
		// The context handed to newSource is captured by the resulting token
		// source and reused for every future mint/refresh for this user. It MUST
		// NOT be this (request-scoped) call's context: the go-sdk cancels the tool
		// handler's context when the call returns, so a captured request context
		// would fail every refresh once the first assertion expires (~1h) — for
		// every caller, until restart. WithoutCancel keeps any values but never
		// cancels or expires. Same reasoning as Personal.GoogleToken.
		s, err := d.newSource(context.WithoutCancel(ctx), user)
		if err != nil {
			d.mu.Unlock()
			return "", err
		}
		src = s
		d.sources[user] = s
	}
	d.mu.Unlock()

	tok, err := src.Token()
	if err != nil {
		return "", fmt.Errorf("dwd: mint token for user: %w", err)
	}
	return tok.AccessToken, nil
}
