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

> **Status: M6 (reads + gated writes/sends + Directory + governance +
> resource-server mode).** Signs you in with your own Google account
> (installed-app OAuth: loopback + PKCE) and acts as you across Gmail, Calendar,
> and Drive: reads by default, gated mutations behind `--allow-writes`, and
> irreversible/egress actions behind a separate `--allow-sends` (dry-run previews
> until a gate opens). Behind `--admin`: Admin SDK Directory reads/writes, audit
> log (Reports), connected-app and license audit. Also runs as a **multi-user
> resource server** (`--http`): it validates each request's bearer token against
> any OIDC IdP (Keycloak, Entra, Google) and acts as the mapped caller via
> domain-wide delegation. See [docs/auth.md](docs/auth.md) for the identity model
> and [docs/capabilities.md](docs/capabilities.md) for the tools. Next: the
> powerful-delegated and powerful-application tiers.

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

Flags: `--allow-writes` (write gate), `--allow-sends` (separate send gate),
`--admin` (register Admin SDK Directory tools; only useful when the signed-in
user is a Workspace/Cloud Identity admin).

| Variable | Purpose |
| --- | --- |
| `GWS_CLIENT_ID` | OAuth client id (GCP "Desktop app" client). Required for the Gmail tools |
| `GWS_CLIENT_SECRET` | The paired client secret. Never logged or returned |
| `GWS_MCP_ALLOW_WRITES` | `true` opens the write gate (same as `--allow-writes`) |
| `GWS_MCP_ALLOW_SENDS` | `true` opens the send gate (same as `--allow-sends`; independent of writes) |

## Running (resource-server mode)

For a shared, multi-user deployment, serve over HTTP and validate each request's
bearer token against your OIDC IdP:

```sh
export GWS_AUDIENCE="api://gws-mcp"            # the audience tokens must carry
export GWS_ISSUERS="https://keycloak.example/realms/main"  # trusted issuer(s), comma-separated
export GWS_DWD_SA_KEY="/run/secrets/dwd-sa.json"           # domain-wide-delegation SA key
./gws-mcp-server --http :8080
```

Each request is verified (signature/issuer/audience/expiry), mapped to a Google
user via a configurable claim (`GWS_SUBJECT_CLAIM`, default `email`), and
impersonated via domain-wide delegation — Google enforces that user's rights.
The DWD key is a domain credential: keep its granted scopes minimal, store it
outside the repo, and run this mode behind an authenticating gateway. See
[docs/auth.md](docs/auth.md).

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
