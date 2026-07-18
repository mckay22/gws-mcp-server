package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/config"
)

func TestResolveBindAddr(t *testing.T) {
	cases := []struct {
		addr            string
		allowNonLoop    bool
		want            string
		wantErrContains string
	}{
		{addr: "8080", want: "127.0.0.1:8080"},
		{addr: ":8080", want: "127.0.0.1:8080"},
		{addr: "127.0.0.1:9000", want: "127.0.0.1:9000"},
		{addr: "localhost:9000", want: "localhost:9000"},
		{addr: "0.0.0.0:9000", wantErrContains: "non-loopback"},
		{addr: "0.0.0.0:9000", allowNonLoop: true, want: "0.0.0.0:9000"},
		{addr: "", wantErrContains: "empty"},
		{addr: "127.0.0.1:", wantErrContains: "missing port"},
	}
	for _, tc := range cases {
		got, err := resolveBindAddr(tc.addr, tc.allowNonLoop)
		if tc.wantErrContains != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("resolveBindAddr(%q, %v) err = %v, want containing %q", tc.addr, tc.allowNonLoop, err, tc.wantErrContains)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveBindAddr(%q, %v) unexpected err %v", tc.addr, tc.allowNonLoop, err)
		}
		if got != tc.want {
			t.Errorf("resolveBindAddr(%q, %v) = %q, want %q", tc.addr, tc.allowNonLoop, got, tc.want)
		}
	}
}

func TestResourceMetadataDocument(t *testing.T) {
	cfg := config.Config{
		Audience:       "api://gws-mcp",
		AllowedIssuers: []string{"https://keycloak.example.com/realms/main", "https://login.microsoftonline.com/tid/v2.0"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	resourceMetadataHandler(cfg)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if doc["resource"] != "api://gws-mcp" {
		t.Errorf("resource = %v", doc["resource"])
	}
	servers, ok := doc["authorization_servers"].([]any)
	if !ok || len(servers) != 2 {
		t.Errorf("authorization_servers = %v, want 2", doc["authorization_servers"])
	}
}

// TestResourceMetadataPathAndURL covers the RFC 9728 §3.1 derivation: the
// well-known segment goes between host and the resource's own path, and only an
// http(s) resource identifier yields a fetchable metadata URL.
func TestResourceMetadataPathAndURL(t *testing.T) {
	cases := []struct {
		audience string
		wantPath string
		wantURL  string
	}{
		{"https://mcp.example.com/mcp", "/.well-known/oauth-protected-resource/mcp", "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"},
		{"https://mcp.example.com", "/.well-known/oauth-protected-resource", "https://mcp.example.com/.well-known/oauth-protected-resource"},
		{"https://mcp.example.com/", "/.well-known/oauth-protected-resource", "https://mcp.example.com/.well-known/oauth-protected-resource"},
		{"https://mcp.example.com/a/b", "/.well-known/oauth-protected-resource/a/b", "https://mcp.example.com/.well-known/oauth-protected-resource/a/b"},
		// Identifier-style audiences have no fetchable metadata location.
		{"api://gws-mcp", "/.well-known/oauth-protected-resource", ""},
		{"gws-mcp", "/.well-known/oauth-protected-resource", ""},
	}
	for _, tc := range cases {
		if got := resourceMetadataPath(tc.audience); got != tc.wantPath {
			t.Errorf("resourceMetadataPath(%q) = %q, want %q", tc.audience, got, tc.wantPath)
		}
		if got := resourceMetadataURL(tc.audience); got != tc.wantURL {
			t.Errorf("resourceMetadataURL(%q) = %q, want %q", tc.audience, got, tc.wantURL)
		}
	}
}

// TestUnauthorizedAdvertisesResourceMetadata is the MCP-authorization compliance
// test: an unauthenticated /mcp request MUST come back 401 carrying a
// WWW-Authenticate header that points at the protected-resource metadata, which
// is how a compliant client discovers where to get a token. The verifier is nil
// because a request with no bearer token is rejected before it is consulted.
func TestUnauthorizedAdvertisesResourceMetadata(t *testing.T) {
	cfg := config.Config{
		Audience:       "https://mcp.example.com/mcp",
		AllowedIssuers: []string{"https://keycloak.example.com/realms/main"},
	}
	handler := mcpHTTPHandler(cfg, nil, nil, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	auth := rec.Header().Get("WWW-Authenticate")
	if auth == "" {
		t.Fatal("401 carries no WWW-Authenticate header (MCP authorization requires one)")
	}
	wantParam := `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`
	if !strings.Contains(auth, wantParam) {
		t.Errorf("WWW-Authenticate = %q, want it to contain %s", auth, wantParam)
	}
}

// TestResourceMetadataServedAtDerivedPath checks the document is reachable at
// both the path-appended location for this audience and the bare well-known
// path, and that it answers a cross-origin preflight (browser MCP clients read
// it from their own origin).
func TestResourceMetadataServedAtDerivedPath(t *testing.T) {
	cfg := config.Config{
		Audience:       "https://mcp.example.com/mcp",
		AllowedIssuers: []string{"https://keycloak.example.com/realms/main"},
	}
	handler := mcpHTTPHandler(cfg, nil, nil, nil)

	for _, path := range []string{
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-protected-resource",
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			t.Errorf("GET %s: decode: %v", path, err)
			continue
		}
		if doc["resource"] != cfg.Audience {
			t.Errorf("GET %s: resource = %v, want %q", path, doc["resource"], cfg.Audience)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("GET %s: Access-Control-Allow-Origin = %q, want *", path, got)
		}
	}

	// CORS preflight.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/.well-known/oauth-protected-resource/mcp", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight = %d, want 204", rec.Code)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "LocalHost", "127.0.0.1", "::1"} {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "10.0.0.5", "example.com"} {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}
