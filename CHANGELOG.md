# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); entries are grouped
by milestone.

## [Unreleased]

### Added

- **M3 — gated writes + sends.** The two-gate safety model ported from
  entra-mcp-server, adapted for Google. `write.go` adds `writePlan`/`runWrite`:
  a tool builds one plan (method, service base, path, query, body) and hands it
  to `runWrite`, which either returns a dry-run preview (the gate's exact env
  var + flag named in the message, the exact request URL and body shown) or
  applies it. Two improvements over the sibling: the dry-run message names the
  *correct* gate (write vs send) for the plan, and an `ApplyBody` field lets a
  tool show a readable preview while sending a different wire form (Gmail's
  base64url raw MIME). Eleven gated tools: Gmail `gmail_create_draft`/
  `gmail_modify` (🟡) and `gmail_send`/`gmail_reply` (🔴, RFC 2822 MIME built
  and base64url-encoded); Calendar `create_appointment` (🟡, no attendees) vs
  `create_event_with_attendees`/`update_event`/`cancel_event`/`respond_to_event`
  (🔴, `sendUpdates=all`) — the attendee split is the gate split; Drive
  `upload_file` (🟡, multipart) and `share_file` (🔴, permission grant = egress).
  Client gains `Patch`, `Delete`, `PostRaw` (custom Content-Type via a
  refactored `doRaw`), a query param on `Post`, and `BaseDriveUpload`. Scopes
  become gate-aware: `gmail.modify`/`calendar.events`/`drive` only when
  `--allow-writes`, `gmail.send` only when `--allow-sends`, so a read-only
  deployment never consents to a mutating scope. Tests: gate independence in
  both directions (the acceptance bar), preview-makes-no-call, secret redaction,
  ApplyBody wire-form substitution, MIME round-trip, and every tool's
  apply/dry-run/validation paths. No new dependencies.

- **M2 — Calendar + Drive reads.** Four Calendar tools (`list_calendars`,
  `list_events`, `get_event`, `freebusy_query`) and two Drive tools
  (`list_files`, `get_file_content`), all acting as the signed-in user. Events
  are windowed (RFC3339 bounds, blank defaults to now / +30 days, malformed
  rejected), expanded with `singleEvents=true` and ordered by start time, one
  bounded page with `nextPageToken`. `freebusy_query` returns availability
  without event details. `list_files` takes Drive's search syntax (recent-first
  default), excludes trash by default, and can span shared drives;
  `get_file_content` exports Google Docs/Sheets/Slides to text/CSV, downloads
  other files directly, rejects text-less binaries, and caps output at 200 KiB.
  Client gains `Post` (JSON body → raw JSON, backing the free/busy read and
  future mutations) and `GetRaw` (raw bytes + Content-Type, backing Drive
  media/export downloads), plus `BaseCalendar`/`BaseDrive`. Scopes add
  `calendar.readonly` and `drive.readonly`. Recording-mock tests cover every
  tool including window defaulting, query wiring, export-vs-download routing,
  and not-found/validation paths. No new dependencies.

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
