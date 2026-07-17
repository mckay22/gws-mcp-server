// Package googleauth provides the classic-delegated (stdio) mode credential
// provider: it signs a single user in via the OAuth installed-app flow (loopback
// redirect + PKCE) and hands out Google API bearer tokens. It is one
// implementation of the gapi TokenSource abstraction — tool code only ever calls
// GoogleToken and never touches golang.org/x/oauth2 directly.
//
// Secrets discipline: a token value is never printed or logged. The sign-in
// instructions (the authorization URL) go to STDERR, because STDOUT is the MCP
// protocol channel.
package googleauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"golang.org/x/oauth2"
)

// googleEndpoint is Google's OAuth 2.0 authorization and token endpoint. It is
// inlined rather than imported from golang.org/x/oauth2/google so M1 does not
// pull that package's cloud-metadata dependency; the /google subpackage joins
// the tree in M5 when service-account JWT signing (DWD) actually needs it.
var googleEndpoint = oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}

// Personal implements the gapi TokenSource interface
//
//	GoogleToken(ctx context.Context) (string, error)
//
// for classic-delegated/stdio mode. The first call runs the interactive
// installed-app OAuth flow (loopback + PKCE) once; afterwards it serves access
// tokens from an in-memory oauth2 source that refreshes transparently using the
// stored refresh token. It is safe for concurrent use.
type Personal struct {
	oauth *oauth2.Config

	// authorize runs the interactive sign-in and returns the initial token. It
	// is a field so tests can substitute a fake and exercise the caching/refresh
	// logic without a browser or a live Google.
	authorize func(ctx context.Context, oauth *oauth2.Config) (*oauth2.Token, error)

	mu  sync.Mutex
	src oauth2.TokenSource // nil until the first successful sign-in
}

// Compile-time assertion that Personal satisfies the gapi TokenSource contract.
// The interface is asserted structurally (not imported) so this package builds
// independently of the sibling gapi package.
var _ interface {
	GoogleToken(ctx context.Context) (string, error)
} = (*Personal)(nil)

// NewPersonal builds the classic-delegated credential provider from config. It
// requires GWS_CLIENT_ID and GWS_CLIENT_SECRET (a GCP "Desktop app" OAuth
// client) and requests the given scopes at sign-in.
//
// It does NOT authenticate: constructing the provider only prepares the OAuth
// config. The actual sign-in (opening the authorization URL) happens lazily on
// the first GoogleToken call, never at startup.
func NewPersonal(cfg config.Config, scopes []string) (*Personal, error) {
	if err := cfg.RequirePersonal(); err != nil {
		return nil, err
	}
	return &Personal{
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     googleEndpoint,
			Scopes:       scopes,
			// RedirectURL is set per sign-in to the ephemeral loopback address.
		},
		authorize: authorizeLoopback,
	}, nil
}

// GoogleToken returns a bearer token for calling the Google APIs as the
// signed-in user. The first call triggers the interactive sign-in; subsequent
// calls serve the in-memory token, refreshing shortly before expiry via the
// stored refresh token. It is goroutine-safe. The returned token is never
// logged.
func (p *Personal) GoogleToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.src == nil {
		tok, err := p.authorize(ctx, p.oauth)
		if err != nil {
			return "", fmt.Errorf("google sign-in: %w", err)
		}
		// oauth2.Config.TokenSource wraps the token in a refreshing, reusable
		// source: it hands back the cached access token until it nears expiry,
		// then refreshes using the refresh token against the token endpoint.
		//
		// The context passed here is captured by the refresher and reused for every
		// future refresh HTTP call. It MUST NOT be this (request-scoped) call's
		// context: the go-sdk cancels the tool handler's context when the call
		// returns, so a captured request context would cancel every refresh after
		// the first access token expires (~1h), silently killing the server until
		// restart. WithoutCancel keeps any values but never cancels or expires.
		p.src = p.oauth.TokenSource(context.WithoutCancel(ctx), tok)
	}

	tok, err := p.src.Token()
	if err != nil {
		return "", fmt.Errorf("refresh google token: %w", err)
	}
	return tok.AccessToken, nil
}

// authorizeLoopback runs the OAuth installed-app flow: it binds an ephemeral
// loopback listener as the redirect target, opens the consent URL (printed to
// stderr and, best-effort, in the system browser), captures the authorization
// code from the redirect, and exchanges it — with the PKCE verifier — for a
// token. The state parameter is verified to defend against cross-site request
// forgery on the redirect.
func authorizeLoopback(ctx context.Context, oauth *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("open loopback listener: %w", err)
	}
	defer ln.Close()
	oauth.RedirectURL = "http://" + ln.Addr().String() + "/"

	verifier := oauth2.GenerateVerifier()
	state, err := randomToken()
	if err != nil {
		return nil, err
	}
	authURL := oauth.AuthCodeURL(state,
		oauth2.AccessTypeOffline, // request a refresh token
		oauth2.ApprovalForce,     // force a consent so a refresh token is issued even on re-auth
		oauth2.S256ChallengeOption(verifier),
	)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("error") != "":
			browserMessage(w, "Sign-in failed: "+q.Get("error")+". You can close this tab.")
			resCh <- result{err: fmt.Errorf("authorization denied: %s", q.Get("error"))}
		case q.Get("state") != state:
			browserMessage(w, "Sign-in failed: state mismatch. You can close this tab.")
			resCh <- result{err: errors.New("authorization state mismatch (possible CSRF)")}
		case q.Get("code") == "":
			browserMessage(w, "Sign-in failed: no authorization code. You can close this tab.")
			resCh <- result{err: errors.New("no authorization code in redirect")}
		default:
			browserMessage(w, "Sign-in complete. You can close this tab and return to your terminal.")
			resCh <- result{code: q.Get("code")}
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "\ngws-mcp-server: open this URL to authorize Google access:\n\n  %s\n\n", authURL)
	_ = openBrowser(authURL) // best effort; the stderr URL is the reliable path

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-resCh:
		if res.err != nil {
			return nil, res.err
		}
		tok, err := oauth.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("exchange authorization code: %w", err)
		}
		return tok, nil
	}
}

// randomToken returns a URL-safe, unpadded 256-bit random string for the OAuth
// state parameter.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// browserMessage writes a minimal HTML page shown in the user's browser after
// the redirect.
func browserMessage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html><body style=\"font-family:sans-serif\"><p>%s</p></body></html>", msg)
}

// openBrowser makes a best-effort attempt to open url in the system browser. Any
// failure is ignored — the authorization URL is always printed to stderr as the
// reliable path, so no browser is required.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default: // linux, bsd, …
		cmd, args = "xdg-open", []string{url}
	}
	return exec.Command(cmd, args...).Start()
}
