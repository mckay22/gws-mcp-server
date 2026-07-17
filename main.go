// Command gws-mcp-server exposes Google Workspace over the Model Context
// Protocol. The MCP protocol owns stdout, so every diagnostic goes to stderr.
//
// M0 serves stdio in classic-delegated mode: you sign in with your own Google
// account and the server acts as you (the sign-in itself lands in M1). The
// resource-server HTTP transport and the powerful tiers are later milestones.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/mckay22/gws-mcp-server/internal/googleauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverName = "gws-mcp-server"

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "server", serverName, "err", err)
		os.Exit(1)
	}
}

// run parses flags, builds the server, and serves stdio until interrupted.
func run() error {
	allowWrites := flag.Bool("allow-writes", false, "enable the write tools (mutations); off = dry-run previews. Also settable via GWS_MCP_ALLOW_WRITES=true.")
	allowSends := flag.Bool("allow-sends", false, "enable send-class tools (irreversible: mail send, sharing); off = dry-run previews. Separate from --allow-writes. Also GWS_MCP_ALLOW_SENDS=true.")
	flag.Parse()

	// Protocol traffic owns stdout; structured diagnostics go to stderr only.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg := config.ConfigFromEnv()
	if *allowWrites {
		cfg.AllowWrites = true // the flag ORs with GWS_MCP_ALLOW_WRITES.
	}
	if *allowSends {
		cfg.AllowSends = true // the flag ORs with GWS_MCP_ALLOW_SENDS.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Startup line: name/version, mode, and config readiness — presence booleans
	// only, never any secret value.
	p := cfg.Presence()
	logger.Info("starting",
		"server", serverName,
		"version", version,
		"transport", "stdio",
		"mode", cfg.Mode(),
		"writes", cfg.AllowWrites,
		"sends", cfg.AllowSends,
		"clientId", p.ClientID,
		"clientSecret", p.ClientSecret,
	)

	return newMCPServer(cfg).Run(ctx, &mcp.StdioTransport{})
}

// newMCPServer builds a server with every available tool registered. health is
// always present. The Gmail read tools require classic-delegated credentials
// (GWS_CLIENT_ID + GWS_CLIENT_SECRET); when they are absent the server still
// starts with just health, and the Gmail tools light up once the OAuth client is
// configured — mirroring the sibling entra-mcp-server. Sign-in itself is lazy:
// it happens on the first Gmail tool call, never here.
func newMCPServer(cfg config.Config) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerHealth(server, cfg)

	creds, err := googleauth.NewPersonal(cfg, requiredScopes(cfg))
	if err != nil {
		// No secret value is ever in this error — it reports missing config only.
		slog.Warn("Gmail tools disabled until credentials are configured", "reason", err)
		return server
	}
	gc := gapi.New(creds)
	// Reads (always on).
	registerGmailReadTools(server, gc)
	registerCalendarReadTools(server, gc)
	registerDriveReadTools(server, gc)
	// Writes/sends (registered always; each tool honors the write or send gate,
	// returning a dry-run preview until its gate is open).
	registerGmailWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerCalendarWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerDriveWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	return server
}

// healthInput has no fields: health takes no arguments.
type healthInput struct{}

// healthOutput is the structured result of the health tool. It reports how the
// server is running and which configuration is present — presence booleans
// only, never any secret value.
type healthOutput struct {
	Server    string          `json:"server" jsonschema:"the MCP server name"`
	Version   string          `json:"version" jsonschema:"the running server version"`
	Transport string          `json:"transport" jsonschema:"the active transport (stdio)"`
	Mode      string          `json:"mode" jsonschema:"the operating mode (classic-delegated)"`
	Writes    bool            `json:"writes" jsonschema:"whether the write tools are enabled (else they dry-run)"`
	Sends     bool            `json:"sends" jsonschema:"whether send-class tools are enabled (else they dry-run)"`
	Config    config.Presence `json:"config" jsonschema:"which GWS_* variables are set (booleans only)"`
}

func registerHealth(server *mcp.Server, cfg config.Config) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "health",
		Title:       "Health check",
		Description: "Report how the server is running: name, version, transport, operating mode, and which GWS_* configuration variables are set (booleans only — never any secret value). Makes no Google API calls.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ healthInput) (*mcp.CallToolResult, healthOutput, error) {
		p := cfg.Presence()
		out := healthOutput{
			Server:    serverName,
			Version:   version,
			Transport: "stdio",
			Mode:      cfg.Mode(),
			Writes:    cfg.AllowWrites,
			Sends:     cfg.AllowSends,
			Config:    p,
		}
		summary := fmt.Sprintf(
			"%s %s ok (transport stdio, mode %s, writes=%t sends=%t; config: clientId=%t clientSecret=%t).",
			serverName, version, out.Mode, out.Writes, out.Sends, p.ClientID, p.ClientSecret)
		return text(summary), out, nil
	})
}

// text builds a CallToolResult carrying a single human-readable line; the typed
// output is attached as StructuredContent by the SDK.
func text(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
