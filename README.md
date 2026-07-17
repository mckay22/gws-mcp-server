# gws-mcp-server

An MCP server that exposes **Google Workspace / Google Cloud Identity** (Gmail,
Calendar, Drive, Directory, audit) to AI assistants as tools — the Google
sibling of [entra-mcp-server](https://github.com/mckay22/entra-mcp-server),
built to the same principles:

- **The platform is the authority.** The server acts as a real Google identity
  (yours, or in later milestones the verified caller's) and lets Google enforce
  authorization on every call — it never builds a parallel permission model.
  Role-based tool menus and deny rules belong to an MCP gateway in front, not
  in here.
- **Read-only by default.** Mutations sit behind `--allow-writes`; sending and
  other irreversible/egress actions behind a separate `--allow-sends` that the
  write gate never implies. Gated tools return dry-run previews instead of
  acting.
- **A single static Go binary** on the official MCP go-sdk. Google APIs are
  called over plain `net/http` with `fields` projection — no generated API
  clients, minimal PII in model context.

> **Status: M0 (scaffold).** Stdio MCP server with a `health` tool, env config
> with presence-only reporting, gate flags, CI. Sign-in (installed-app OAuth)
> and Gmail reads land next; then Calendar + Drive reads, the gated
> write/send tools, Directory + audit (governance) reads, and a multi-user
> resource-server mode validating bearer tokens from any OIDC IdP.

## Running

```sh
go build .
./gws-mcp-server
```

The server speaks MCP over stdio; diagnostics go to stderr. Until M1 the only
tool is `health`, which reports version, mode, gate state, and which
configuration variables are set (booleans only — never a value).

Flags: `--allow-writes` (write gate), `--allow-sends` (separate send gate).

| Variable | Purpose |
| --- | --- |
| `GWS_CLIENT_ID` | OAuth client id (GCP "Desktop app" client) — consumed from M1; create your own in your own GCP project |
| `GWS_CLIENT_SECRET` | The paired client secret. Never logged or returned |
| `GWS_MCP_ALLOW_WRITES` | `true` opens the write gate (same as `--allow-writes`) |
| `GWS_MCP_ALLOW_SENDS` | `true` opens the send gate (same as `--allow-sends`; independent of writes) |

## Development

```sh
gofmt -l .
go vet ./...
go test ./...
```

## License

Apache-2.0 — see [LICENSE](LICENSE).
