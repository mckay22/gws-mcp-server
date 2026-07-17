package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectServer wires newMCPServer(cfg) to an in-process client session over
// the SDK's in-memory transport pair.
func connectServer(t *testing.T, cfg config.Config) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := newMCPServer(cfg)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "t"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool invokes a tool, fails on any error or tool error, and returns the
// raw result plus the decoded structured content as a generic map.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) tool error: %v", name, res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal structured: %v", err)
	}
	return res, m
}

func TestHealth(t *testing.T) {
	cs := connectServer(t, config.Config{
		ClientID:     "1234567890-abc.apps.googleusercontent.com",
		ClientSecret: "test-secret-value",
	})

	_, out := callTool(t, cs, "health", map[string]any{})
	if out["server"] != serverName {
		t.Errorf("server = %v, want %s", out["server"], serverName)
	}
	if out["version"] != version {
		t.Errorf("version = %v, want %s", out["version"], version)
	}
	if out["transport"] != "stdio" {
		t.Errorf("transport = %v, want stdio", out["transport"])
	}
	if out["mode"] != config.ModeClassicDelegated {
		t.Errorf("mode = %v, want %s", out["mode"], config.ModeClassicDelegated)
	}
	if out["writes"] != false || out["sends"] != false {
		t.Errorf("gates = writes:%v sends:%v, want both false by default", out["writes"], out["sends"])
	}
	presence, ok := out["config"].(map[string]any)
	if !ok {
		t.Fatalf("config = %v, want object", out["config"])
	}
	if presence["clientId"] != true || presence["clientSecret"] != true {
		t.Errorf("presence = %v, want both true", presence)
	}
}

func TestHealthGatesIndependent(t *testing.T) {
	// Opening the write gate must not open the send gate.
	cs := connectServer(t, config.Config{AllowWrites: true})

	_, out := callTool(t, cs, "health", map[string]any{})
	if out["writes"] != true {
		t.Errorf("writes = %v, want true", out["writes"])
	}
	if out["sends"] != false {
		t.Errorf("sends = %v, want false — the write gate must not imply the send gate", out["sends"])
	}
	presence, ok := out["config"].(map[string]any)
	if !ok {
		t.Fatalf("config = %v, want object", out["config"])
	}
	if presence["clientId"] != false || presence["clientSecret"] != false {
		t.Errorf("presence = %v, want both false with no credentials", presence)
	}
}

func TestHealthNeverLeaksSecrets(t *testing.T) {
	const secret = "extremely-secret-value-8f3a"
	cs := connectServer(t, config.Config{
		ClientID:     "1234567890-abc.apps.googleusercontent.com",
		ClientSecret: secret,
	})

	res, _ := callTool(t, cs, "health", map[string]any{})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("health result contains the client secret value")
	}
	// The human-readable line still reports presence.
	if !strings.Contains(string(raw), "clientSecret=true") {
		t.Errorf("health text should report clientSecret presence, got: %s", raw)
	}
}
