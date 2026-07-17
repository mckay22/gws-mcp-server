# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); entries are grouped
by milestone.

## [Unreleased]

### Added

- **M1 — classic-delegated mode + Gmail reads.** Installed-app OAuth sign-in
  (`internal/googleauth`): loopback redirect + PKCE (S256), CSRF-checked state,
  offline access for a refresh token, authorization URL to stderr with
  best-effort browser open. Sign-in is lazy (first tool call, never at startup)
  and the token is held in memory, refreshing transparently via oauth2's
  reusable token source. Generic Google REST client (`internal/gapi`): a thin
  `net/http` client over the several Google API hosts (scheme+host rewritable
  for tests), `nextPageToken` paging with a per-API items-field, `fields`
  projection, Google error-envelope decoding, and central backoff on 429/503
  and rate-limit 403s honoring `Retry-After`. Five Gmail read tools (act as the
  signed-in user against `/users/me`): `get_profile`, `list_labels`,
  `list_messages`, `search_messages` (Gmail `q` syntax), and `get_message`
  (`metadata`/`full`, decoded plain-text body capped at 100 KiB, thread ids
  surfaced). Config gains `RequirePersonal` and the `GWS_CLIENT_ID` /
  `GWS_CLIENT_SECRET` variables; capabilities doc added. Tests use recording
  HTTP mocks (no live Google): client paging/backoff/error decoding, the
  token-source sign-in-once-then-refresh path, and every tool including query
  wiring, format validation, and not-found handling. New dependency:
  `golang.org/x/oauth2` (the OAuth flow and refreshing token source) — the
  `/google` subpackage is deliberately deferred to M5 (DWD JWT signing) to keep
  M1's dependency tree minimal; Google's OAuth endpoint is inlined instead.

- **M0 — scaffold.** Go module and stdio MCP server on the official go-sdk,
  with a `health` tool reporting name/version/transport/mode, gate state, and
  GWS_* config presence (booleans only — never a value). Env-driven config
  (`internal/config`) with strict `true`-only gate parsing, a value-free
  `Presence` struct, and a `Redact` helper; `--allow-writes` / `--allow-sends`
  flags OR with their env vars (contract only — no gated tools yet). Stderr
  slog (stdout belongs to the protocol), CI (gofmt/vet/test), Apache-2.0
  license. Tests cover config parsing, gate independence, and a
  secret-never-leaks assertion over the full `health` result. Dependency:
  `github.com/modelcontextprotocol/go-sdk` v1.6.1 (the MCP protocol
  implementation; same version as the sibling entra-mcp-server).
