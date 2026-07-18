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
	httpAddr := flag.String("http", "", "serve over streamable HTTP at this address (resource-server mode; requires GWS_AUDIENCE, GWS_ISSUERS, GWS_DWD_SA_KEY). Default is stdio (classic-delegated).")
	allowWrites := flag.Bool("allow-writes", false, "enable the write tools (mutations); off = dry-run previews. Also settable via GWS_MCP_ALLOW_WRITES=true.")
	allowSends := flag.Bool("allow-sends", false, "enable send-class tools (irreversible: mail send, sharing); off = dry-run previews. Separate from --allow-writes. Also GWS_MCP_ALLOW_SENDS=true.")
	admin := flag.Bool("admin", false, "register the Admin SDK Directory tools and request admin.directory.*.readonly scopes; only useful when the signed-in user is an admin. Also GWS_MCP_ADMIN=true.")
	powerful := flag.Bool("powerful", false, "register the powerful-delegated end-user tools (Gmail settings, Tasks, People, Chat, Meet, Drive shared-with-me); they still honor the write/send gates. Also GWS_MCP_POWERFUL=true.")
	appOnly := flag.Bool("app-only", false, "register the powerful-application tier: app_* tools acting on an explicit user target via a SEPARATE service account (GWS_APP_SA_KEY, which must differ from GWS_DWD_SA_KEY). They still honor the write/send gates. Also GWS_MCP_APP_ONLY=true.")
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
	if *admin {
		cfg.Admin = true // the flag ORs with GWS_MCP_ADMIN.
	}
	if *powerful {
		cfg.Powerful = true // the flag ORs with GWS_MCP_POWERFUL.
	}
	if *appOnly {
		cfg.AppOnly = true // the flag ORs with GWS_MCP_APP_ONLY.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *httpAddr != "" {
		// Resource-server mode: serveHTTP validates config, binds, and logs.
		return serveHTTP(ctx, *httpAddr, cfg)
	}

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
		"admin", cfg.Admin,
		"clientId", p.ClientID,
		"clientSecret", p.ClientSecret,
	)

	server, err := newMCPServer(cfg)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

// newMCPServer builds a server with every available tool registered. health is
// always present. The delegated tools require classic-delegated credentials
// (GWS_CLIENT_ID + GWS_CLIENT_SECRET); when they are absent the server still
// starts with just health (and any app tier), and they light up once the OAuth
// client is configured — mirroring the sibling entra-mcp-server. Sign-in is lazy:
// it happens on the first delegated tool call, never here.
//
// The powerful-application tier is different: it is an explicit opt-in
// (--app-only), so when requested-but-misconfigured the server fails to start
// rather than silently running without it. It gets its OWN Google client over
// its OWN service-account credential — never the delegated one — and is usable
// standalone (no delegated credentials required).
func newMCPServer(cfg config.Config) (*mcp.Server, error) {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerHealth(server, cfg, "stdio")

	if cfg.AppOnly {
		appGC, err := buildAppClient(cfg)
		if err != nil {
			return nil, err
		}
		registerAppTools(server, appGC, cfg)
	}

	creds, err := googleauth.NewPersonal(cfg, requiredScopes(cfg))
	if err != nil {
		// No secret value is ever in this error — it reports missing config only.
		slog.Warn("delegated tools disabled until credentials are configured", "reason", err)
		return server, nil
	}
	registerTools(server, gapi.New(creds), cfg)
	return server, nil
}

// buildAppClient constructs the powerful-application tier's own Google client
// from its own service-account credential, enforcing the separate-credential
// rule (its key must differ from the resource-server DWD key). A misconfiguration
// is a startup error, never a silent skip.
func buildAppClient(cfg config.Config) (*gapi.Client, error) {
	if err := cfg.RequireAppOnly(); err != nil {
		return nil, fmt.Errorf("app-only tier requested but misconfigured: %w", err)
	}
	appDWD, err := googleauth.NewDWD(cfg.AppKeyPath, appScopes(cfg))
	if err != nil {
		return nil, fmt.Errorf("app-only tier requested but misconfigured: %w", err)
	}
	return gapi.New(appDWD), nil
}

// registerTools installs every Google-backed tool on the server: the read tools
// (always on), the write/send tools (registered always, each honoring its gate),
// and the Admin SDK Directory reads (only when --admin). Both transports — stdio
// (classic-delegated) and HTTP (resource-server) — call it against their own
// gapi.Client, so the tool surface is identical across modes.
func registerTools(server *mcp.Server, gc *gapi.Client, cfg config.Config) {
	// Reads (always on).
	registerGmailReadTools(server, gc)
	registerCalendarReadTools(server, gc)
	registerDriveReadTools(server, gc)
	// Writes/sends (registered always; each tool honors the write or send gate,
	// returning a dry-run preview until its gate is open).
	registerGmailWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerCalendarWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerDriveWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	// Admin SDK tools (opt-in registration via --admin): Directory reads,
	// governance reads (audit/tokens/licenses), and Directory writes (the write
	// tools honor the write gate).
	if cfg.Admin {
		registerDirectoryReadTools(server, gc)
		registerGovernanceReadTools(server, gc)
		registerDirectoryWriteTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	}
	// Powerful-delegated end-user tier (opt-in registration via --powerful; each
	// tool still honors the write/send gates).
	if cfg.Powerful {
		registerPowerfulTools(server, gc, cfg.AllowWrites, cfg.AllowSends)
	}
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
	Admin     bool            `json:"admin" jsonschema:"whether the Admin SDK Directory/governance tools are registered"`
	Powerful  bool            `json:"powerful" jsonschema:"whether the powerful-delegated end-user tools are registered"`
	AppOnly   bool            `json:"appOnly" jsonschema:"whether the powerful-application (app_*) tools are registered"`
	Config    config.Presence `json:"config" jsonschema:"which GWS_* variables are set (booleans only)"`
}

func registerHealth(server *mcp.Server, cfg config.Config, transport string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "health",
		Annotations: localAnnotations(),
		Title:       "Health check",
		Description: "Report how the server is running: name, version, transport, operating mode, and which GWS_* configuration variables are set (booleans only — never any secret value). Makes no Google API calls.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ healthInput) (*mcp.CallToolResult, healthOutput, error) {
		p := cfg.Presence()
		out := healthOutput{
			Server:    serverName,
			Version:   version,
			Transport: transport,
			Mode:      cfg.Mode(),
			Writes:    cfg.AllowWrites,
			Sends:     cfg.AllowSends,
			Admin:     cfg.Admin,
			Powerful:  cfg.Powerful,
			AppOnly:   cfg.AppOnly,
			Config:    p,
		}
		summary := fmt.Sprintf(
			"%s %s ok (transport %s, mode %s, writes=%t sends=%t admin=%t powerful=%t appOnly=%t; config: clientId=%t clientSecret=%t).",
			serverName, version, out.Transport, out.Mode, out.Writes, out.Sends, out.Admin, out.Powerful, out.AppOnly, p.ClientID, p.ClientSecret)
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
