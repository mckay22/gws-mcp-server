package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/mckay22/gws-mcp-server/internal/googleauth"
	"github.com/mckay22/gws-mcp-server/internal/oidcauth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serveHTTP runs the resource-server MCP transport over streamable HTTP at /mcp.
//
// This is the multi-user mode: every request must carry a bearer token minted for
// this server (audience = cfg.Audience) by a trusted OIDC issuer (cfg.Issuers()).
// The token is validated against the issuer's JWKS, mapped to a Google user via
// the configured claim, and that user is impersonated through the domain-wide
// delegation (DWD) backend so each tool calls Google as that caller. Google, not
// this server, remains the authority on what the caller may do. Because every
// request is authenticated, binding a network-reachable address is allowed.
//
// The caller owns the lifecycle: serveHTTP shuts down gracefully when ctx is
// cancelled (the main thread wires ctx to os.Interrupt).
func serveHTTP(ctx context.Context, addr string, cfg config.Config) error {
	if err := cfg.RequireResourceServer(); err != nil {
		return err
	}

	verifier, err := oidcauth.NewVerifier(ctx, cfg.Issuers(), cfg.Audience, cfg.SubjectClaimOrDefault(), !cfg.TrustUnverifiedEmail)
	if err != nil {
		return fmt.Errorf("configuring OIDC token verifier: %w", err)
	}

	// One Google client serves every caller: the DWD token source reads the
	// per-request impersonation target from the context (userMiddleware puts it
	// there), so a single client acts as whichever user made the call without
	// leaking tokens between them.
	dwd, err := googleauth.NewDWD(cfg.DWDKeyPath, requiredScopes(cfg))
	if err != nil {
		return fmt.Errorf("configuring DWD identity backend: %w", err)
	}
	gc := gapi.New(dwd)

	// The powerful-application tier is an explicit opt-in with its OWN service
	// account; requested-but-broken config fails startup rather than silently
	// running without it.
	var appGC *gapi.Client
	if cfg.AppOnly {
		appGC, err = buildAppClient(cfg)
		if err != nil {
			return err
		}
	}

	bind, err := resolveBindAddr(addr, cfg.ResourceServerMode())
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              bind,
		Handler:           mcpHTTPHandler(cfg, verifier, gc, appGC),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down gracefully when the caller's context is cancelled (signal).
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	// Diagnostics go to stderr. The audience is public (it is published in the
	// resource metadata); no secret is logged.
	slog.Info("starting",
		"server", serverName,
		"version", version,
		"transport", "http",
		"endpoint", "http://"+bind+"/mcp",
		"mode", config.ModeResourceServer,
		"audience", cfg.Audience,
		"issuers", len(cfg.Issuers()),
		"subjectClaim", cfg.SubjectClaimOrDefault(),
		"dwdKey", cfg.Presence().DWDKey,
	)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// mcpHTTPHandler builds the resource-server HTTP handler: the streamable MCP
// endpoint at /mcp guarded by bearer-token verification, plus the RFC 9728
// Protected Resource Metadata document at /.well-known/oauth-protected-resource.
//
// Every /mcp request is verified (bearerVerifier); the verified caller's mapped
// Google user is then carried into each tool call's context (userMiddleware) so
// the DWD backend impersonates that user. The metadata endpoint is deliberately
// unauthenticated so a client can discover where to obtain a token.
func mcpHTTPHandler(cfg config.Config, verifier *oidcauth.Verifier, gc *gapi.Client, appGC *gapi.Client) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerHealth(server, cfg, "http")
	registerTools(server, gc, cfg)
	if appGC != nil {
		// The application tier's own client (its own SA), separate from the
		// delegated one; its app_* tools log each applied mutation with the
		// verified caller as the requesting actor.
		registerAppTools(server, appGC, cfg)
	}
	server.AddReceivingMiddleware(userMiddleware)

	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			SessionTimeout: 5 * time.Minute,
			Logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		},
	)

	// Cross-origin protection (CSRF / DNS-rebinding defence in depth). The SDK
	// applies none by default in v1.6.x and its own option is deprecated in favor
	// of wrapping the handler, which is what this does. Safe methods and requests
	// with neither Sec-Fetch-Site nor Origin (i.e. non-browser clients) are
	// allowed through, so ordinary MCP clients are unaffected.
	protected := http.NewCrossOriginProtection().Handler(streamable)

	// RFC 9728 / MCP authorization: a 401 MUST carry a WWW-Authenticate header
	// naming this server's protected-resource metadata, which is how a compliant
	// client discovers where to obtain a token. Scopes are deliberately NOT
	// enforced here — Google remains the authority on what the caller may do, and
	// this server builds no parallel permission model; the advertised scope is
	// informational, in the metadata document only.
	handler := sdkauth.RequireBearerToken(bearerVerifier(verifier), &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: resourceMetadataURL(cfg.Audience),
	})(protected)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)

	// Serve the metadata document at the RFC 9728 path-appended location derived
	// from the resource identifier (e.g. audience https://host/mcp is described at
	// /.well-known/oauth-protected-resource/mcp) and, when that differs, at the
	// bare well-known path too — so a client deriving either form finds it.
	metadata := resourceMetadataHandler(cfg)
	for _, p := range metadataPaths(cfg.Audience) {
		mux.HandleFunc("GET "+p, metadata)
		mux.HandleFunc("OPTIONS "+p, metadata) // CORS preflight for browser clients
	}
	return mux
}

// defaultResourceMetadataPath is the RFC 9728 well-known path used when the
// resource identifier carries no path component (or is not a URL).
const defaultResourceMetadataPath = "/.well-known/oauth-protected-resource"

// metadataPaths lists the paths the metadata document is served at: the
// path-appended location for this audience first, plus the bare well-known path
// when it differs.
func metadataPaths(audience string) []string {
	p := resourceMetadataPath(audience)
	if p == defaultResourceMetadataPath {
		return []string{p}
	}
	return []string{p, defaultResourceMetadataPath}
}

// resourceMetadataPath derives the RFC 9728 §3.1 well-known path for a resource
// identifier: the well-known segment is inserted between the host and the
// resource's own path, so https://host/mcp is described at
// /.well-known/oauth-protected-resource/mcp. An audience with no path — or one
// that is an opaque identifier rather than a URL — uses the bare path.
func resourceMetadataPath(audience string) string {
	u, err := url.Parse(strings.TrimSpace(audience))
	if err != nil || !u.IsAbs() {
		return defaultResourceMetadataPath
	}
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return defaultResourceMetadataPath
	}
	return defaultResourceMetadataPath + p
}

// resourceMetadataURL renders the absolute URL of this server's metadata
// document, for the WWW-Authenticate header on a 401. It is derivable only from
// an http(s) audience — the MCP-canonical form, where the resource identifier is
// the server's own URL. An identifier-style audience (e.g. "api://gws-mcp") is
// not fetchable, so it yields "" and the SDK omits the parameter rather than
// advertising a location no client could resolve.
func resourceMetadataURL(audience string) string {
	u, err := url.Parse(strings.TrimSpace(audience))
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host + resourceMetadataPath(audience)
}

// userKey is the TokenInfo.Extra key under which bearerVerifier stashes the
// caller's mapped Google user for userMiddleware to lift into the request
// context.
const userKey = "googleUser"

// bearerVerifier verifies an incoming bearer token and, on success, returns the
// TokenInfo the transport binds to the session. The mapped Google user is stashed
// in Extra so userMiddleware can hand it to the DWD backend. Any verification
// failure is wrapped as ErrInvalidToken, which the SDK surfaces as a 401. A token
// that verifies but carries no mappable user claim is rejected — there is no
// caller to act as.
func bearerVerifier(v *oidcauth.Verifier) sdkauth.TokenVerifier {
	return func(ctx context.Context, rawToken string, _ *http.Request) (*sdkauth.TokenInfo, error) {
		claims, err := v.Verify(ctx, rawToken)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", sdkauth.ErrInvalidToken, err)
		}
		if claims.GoogleUser == "" {
			return nil, fmt.Errorf("%w: token has no mappable user claim", sdkauth.ErrInvalidToken)
		}
		return &sdkauth.TokenInfo{
			// UserID binds the session to this subject so the transport rejects a
			// different user's token reusing the session (hijack prevention).
			UserID:     claims.Subject,
			Expiration: claims.Expiry,
			Extra: map[string]any{
				userKey: claims.GoogleUser,
			},
		}, nil
	}
}

// userMiddleware copies the verified caller's mapped Google user from the
// per-request TokenInfo into the context, so the DWD backend impersonates that
// user. With no token on the request (there is none in stdio mode) it is a no-op,
// so the same tool code serves both modes unchanged.
func userMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if extra := req.GetExtra(); extra != nil && extra.TokenInfo != nil {
			if u, ok := extra.TokenInfo.Extra[userKey].(string); ok {
				// The mapped caller is both the delegated impersonation target and
				// the requesting actor for application-tier mutation logging. App
				// tools override the impersonation target per call but keep the
				// actor, so audit records the human who asked.
				ctx = googleauth.WithUser(ctx, u)
				ctx = withActor(ctx, u)
			}
		}
		return next(ctx, method, req)
	}
}

// resourceScopes is advertised in the RFC 9728 scopes_supported field: the
// conventional "act as the signed-in user" scope. It is informational — this
// server enforces access through token validation and Google, never a local
// scope check.
var resourceScopes = []string{"access_as_user"}

// resourceMetadataHandler serves the RFC 9728 Protected Resource Metadata
// document describing this server as an OAuth 2.0 protected resource: the resource
// identifier (the audience tokens must be minted for) and the authorization
// servers (issuers) whose tokens it accepts. It carries no secret and needs no
// authentication.
// The document is public by definition, so it answers cross-origin reads: a
// browser-based MCP client must be able to fetch it from its own origin to run
// discovery.
func resourceMetadataHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":                 cfg.Audience,
			"authorization_servers":    cfg.Issuers(),
			"bearer_methods_supported": []string{"header"},
			"scopes_supported":         resourceScopes,
		})
	}
}

// resolveBindAddr normalizes a --http bind address. A missing host defaults to
// 127.0.0.1. A non-loopback host is rejected unless allowNonLoopback is set
// (which happens only in resource-server mode, where every request is
// authenticated).
func resolveBindAddr(addr string, allowNonLoopback bool) (string, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return "", fmt.Errorf("empty --http address")
	}
	// Allow a bare port (e.g. "8080").
	if !strings.Contains(a, ":") {
		a = ":" + a
	}

	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return "", fmt.Errorf("invalid --http address %q: %w", addr, err)
	}
	if port == "" {
		return "", fmt.Errorf("invalid --http address %q: missing port", addr)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if !isLoopbackHost(host) && !allowNonLoopback {
		return "", fmt.Errorf(
			"refusing to bind --http to non-loopback host %q: HTTP has no per-request authentication "+
				"unless resource-server mode is configured (%s + %s); otherwise use 127.0.0.1, ::1, or localhost",
			host, config.EnvAudience, config.EnvIssuers)
	}
	return net.JoinHostPort(host, port), nil
}

// isLoopbackHost reports whether host is a loopback address or "localhost".
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
