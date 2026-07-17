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
