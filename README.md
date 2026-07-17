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

> **Status: M3 (classic-delegated reads + gated writes/sends).** Stdio MCP
> server that signs you in with your own Google account (installed-app OAuth:
> loopback + PKCE) and acts as you across Gmail, Calendar, and Drive: reads by
> default, plus gated mutations (drafts, labels, uploads) behind
> `--allow-writes` and irreversible/egress actions (mail send/reply, invites,
> sharing) behind a separate `--allow-sends`. Gated tools return dry-run
> previews until their gate is open. Next: Directory + audit (governance) reads,
> and a multi-user resource-server mode validating bearer tokens from any OIDC
> IdP. See [docs/capabilities.md](docs/capabilities.md) for the tool inventory.

## Running (classic-delegated mode)

You need a Google Cloud OAuth client of type **Desktop app** (create one in your
own GCP project — there is no shared client for an open-source server requesting
sensitive scopes). Then:

```sh
go build .
export GWS_CLIENT_ID="…apps.googleusercontent.com"
export GWS_CLIENT_SECRET="…"
./gws-mcp-server
```

The server speaks MCP over stdio; diagnostics go to stderr. Sign-in is **lazy**:
nothing happens at startup, and on the first Gmail tool call the server prints an
authorization URL to stderr (and opens your browser). You approve, Google
redirects to a loopback address, and the token is held in memory for the
process's lifetime. With no credentials configured the server still starts, with
only the `health` tool registered.

Flags: `--allow-writes` (write gate), `--allow-sends` (separate send gate) —
no gated tools exist yet; the flags establish the contract.

| Variable | Purpose |
| --- | --- |
| `GWS_CLIENT_ID` | OAuth client id (GCP "Desktop app" client). Required for the Gmail tools |
| `GWS_CLIENT_SECRET` | The paired client secret. Never logged or returned |
| `GWS_MCP_ALLOW_WRITES` | `true` opens the write gate (same as `--allow-writes`) |
| `GWS_MCP_ALLOW_SENDS` | `true` opens the send gate (same as `--allow-sends`; independent of writes) |

Testable for free on a consumer `@gmail.com`: create the OAuth client in
*Testing* mode (no app verification needed for your own account) and add
yourself as a test user.

## Development

```sh
gofmt -l .
go vet ./...
go test ./...
```

Unit tests use recording HTTP mocks (no live Google, no credentials). A live
smoke test against a real `@gmail.com` is a manual step.

## License

Apache-2.0 — see [LICENSE](LICENSE).
