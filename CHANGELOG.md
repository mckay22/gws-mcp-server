# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); entries are grouped
by milestone.

## [Unreleased]

### Fixed / Security (post-M9 review)

A security- and correctness-focused review of the auth path (independently plus
an adversarial reviewer) found the scariest surfaces solid — the OIDC verifier
core, DWD per-user token isolation, gapi retry/paging bounds, the two-gate
model, and cross-caller identity isolation in resource-server mode — and turned
up five fixes:

- **(High) Classic-delegated token refresh no longer dies after ~1h.** The
  refreshing OAuth token source was built from the first tool call's
  request-scoped context, which the MCP SDK cancels when that call returns; once
  the initial access token expired, every refresh failed with `context canceled`
  and the server was silently dead until restart. It now builds the source from
  a non-cancellable context (`context.WithoutCancel`). Regression-tested against
  a fake token endpoint with the first request's context cancelled.
- **(Medium) DWD impersonation now requires a verified email.** When the subject
  claim is `email`, the resource-server verifier rejects a token whose
  `email_verified` is not `true` — an unverified/mutable email could otherwise
  let a caller be impersonated as an arbitrary Workspace user. Opt out with
  `GWS_TRUST_UNVERIFIED_EMAIL=true` only when every trusted issuer guarantees
  verified emails. Non-`email` subject claims (operator-chosen) are unaffected.
- **(Medium) Gmail MIME builder rejects header injection.** Recipient addresses
  and the `In-Reply-To` value are validated for CR/LF before being written into
  message headers, so a caller cannot smuggle an extra header (e.g. a hidden
  `Bcc:` for exfiltration) — relevant under the MCP prompt-injection threat
  model. The subject was already safe (Q-encoded); the body is unaffected.
- **(Low) Application-tier scopes follow least privilege.** The app-tier service
  account no longer requests directory-write scopes when `--allow-writes` is
  off, matching `requiredScopes` — a read-only application deployment mints no
  token carrying write capability.
- **(Low) `gapi.GetRaw` documents its silent 8 MiB truncation** so a future
  full-download caller cannot mistake a capped body for the complete object (the
  current caller re-caps far below and is unaffected).

Known follow-up (robustness, low): the DWD SA token exchange runs without a
request-scoped timeout; a hung Google token endpoint would block the call. Not
yet addressed.

### Added

- **M9 — packaging & polish.** A `Dockerfile` (multi-stage: Go build → `scratch`
  with CA certs, static `CGO_ENABLED=0` binary, unprivileged uid) plus
  `.dockerignore`; a `release.yml` workflow that cross-compiles six targets
  (linux/darwin/windows × amd64/arm64), archives them with SHA-256 checksums, and
  publishes a GitHub release on a `v*` tag. New docs: `docs/quickstart.md` ($0
  setup walkthroughs for all three tiers — consumer OAuth client, Cloud Identity
  Free, domain-wide delegation, Docker) and `SECURITY.md` (posture: delegated by
  default, the two-gate model, least-privilege scopes, resource-server token
  validation with no pass-through, credential handling). On batching: the
  deprecated global-batch endpoint is not used, and per-API homogeneous batch was
  evaluated and deferred — the bulk application-tier tools already provide
  per-item outcomes via independent sequential calls, which is portable across
  APIs and correct at this scale; per-API batch remains a future optimization.
  No new dependencies. **This completes the PLAN.md roadmap.**

- **M8 — powerful-application tier (`--app-only`).** A tier whose `app_*` tools
  take a required `user` target and act on that principal via a SEPARATE
  service account's domain-wide delegation, reusing the DWD backend by injecting
  the target into each call's context. Startup enforces the separation: the app
  key (`GWS_APP_SA_KEY`) must differ from the resource-server DWD key, and a
  requested-but-misconfigured tier is fatal (never a silent skip) — in both stdio
  and HTTP transports, over the app tier's own `gapi.Client`. Per-user tools:
  `app_list_messages`, `app_get_message`, `app_send_mail` (🔴),
  `app_list_events`, `app_list_files`, `app_set_vacation` (🟡) — each
  impersonating its own target (`fetchMessageDetail`, `listMessages`,
  `listEventsFor` were extracted to serve both `me` and explicit users). Bulk
  Directory lifecycle: `app_bulk_user_suspend` / `app_bulk_group_add_members`
  (🟡) — impersonating the configured admin (`GWS_APP_ADMIN_SUBJECT`), with
  per-item outcomes (one failure never aborts the batch) and up-front
  duplicate-target rejection. **Requesting-actor logging** (`actor.go`): every
  applied application-tier mutation is logged with the verified caller (resource
  server) or `local` (stdio); the resource-server middleware now stamps the actor
  alongside the impersonation target. Config gains `GWS_MCP_APP_ONLY` /
  `GWS_APP_SA_KEY` / `GWS_APP_ADMIN_SUBJECT`, `RequireAppOnly` (separation),
  `appScopes`, and app-key presence; health reports `appOnly`. Tests: target
  impersonation, the send gate on `app_send_mail`, bulk per-item outcomes,
  duplicate rejection, dry-run-makes-no-call, admin-subject requirement, and the
  key-separation rule. No new dependencies.

- **M7 — powerful-delegated tier (`--powerful`).** Fourteen end-user tools
  behind a registration switch (`GWS_MCP_POWERFUL`), each still honoring the
  write/send gates. Gmail settings: `gmail_get_vacation` / `gmail_set_vacation`
  (🟡, the out-of-office analog, via the new client `Put`), `gmail_list_filters`,
  `gmail_list_send_as`. Tasks: `tasks_list_tasklists`, `tasks_list`,
  `tasks_create` (🟡), `tasks_complete` (🟡). People: `people_search_contacts`.
  Chat (Workspace-only): `chat_list_spaces`, `chat_list_messages`,
  `chat_send_message` (🔴). `meet_conference_records` (edition-gated, errors
  cleanly). `drive_shared_with_me`. Client gains `Put` (and a PUT case in
  `runWrite`) plus `BaseTasks`/`BasePeople`/`BaseChat`/`BaseMeet`. Scopes are
  requested only under `--powerful`: `gmail.settings.basic`, `tasks.readonly`
  (and `tasks` under `--allow-writes`), `contacts.readonly`, `chat.spaces`/
  `messages.readonly` (and `chat.messages.create` under `--allow-sends`),
  `meetings.space.readonly`. health reports `powerful`. Recording-mock tests
  cover every tool across all five new API hosts, including the vacation write
  gate, task-create gate, and the Chat send gate (send-gated, not write-gated).
  No new dependencies.

- **M6 — governance (audit, connected-app & license reads; Directory writes).**
  Three read tools (register under `--admin`): `audit_activities` (Reports API
  activity log for login/admin/drive/token/… with time/actor/IP, RFC3339 bounds
  validated, edition-gated apps erroring cleanly), `user_connected_apps`
  (Directory `tokens.list` — the connected-app/consent audit with granted
  scopes), and `license_assignments` (Enterprise License Manager, per
  product/SKU). Six write tools (register under `--admin`, ride the write gate):
  `directory_user_create` (password **redacted** in preview via PreviewBody while
  ApplyBody carries the real value), `directory_user_update`,
  `directory_user_suspend`, `directory_group_create`,
  `directory_group_add_member`, `directory_group_remove_member`. Scopes:
  `admin.reports.audit.readonly`, `admin.directory.user.security`,
  `apps.licensing` added under `--admin`; the read-write directory scopes
  (`admin.directory.user`/`group`/`group.member`) only under `--admin` AND
  `--allow-writes`. `BaseReports`/`BaseLicensing` added. Recording-mock tests
  cover the governance reads (query wiring, validation) and the directory writes
  (password never leaks in preview or applied output, the wire carries it, gate
  and role validation, DELETE path). No new dependencies.

- **M5 — resource-server mode.** A multi-user HTTP transport (`--http <addr>`
  with `GWS_AUDIENCE`) that validates each request's bearer token and acts as the
  mapped caller. New generic OIDC verifier (`internal/oidcauth`): issuer-agnostic
  (Keycloak / Entra / Google, any OIDC IdP), discovering each trusted issuer's
  metadata + JWKS and checking signature, issuer allowlist, audience, and expiry
  — a generalization of the sibling's tenant-specific verifier, mapping the
  caller through a configurable claim (`GWS_SUBJECT_CLAIM`, default `email`). New
  DWD identity backend (`internal/googleauth` `DWD`): the Google analog of the
  On-Behalf-Of exchange — mints a service-account-signed JWT with `sub=<verified
  user>` (domain-wide delegation) and caches a refreshing token source per user.
  `serve.go` wires the streamable HTTP handler behind bearer verification, lifts
  the mapped user into each call's context, publishes RFC 9728 Protected Resource
  Metadata, and refuses a non-loopback bind unless resource-server mode is
  configured. Tool registration is factored into one `registerTools` shared by
  both transports, so the surface is identical across modes; health reports the
  real transport. Config gains `GWS_AUDIENCE` / `GWS_ISSUERS` / `GWS_DWD_SA_KEY`
  / `GWS_SUBJECT_CLAIM`, `ResourceServerMode`, `RequireResourceServer`, and DWD
  key presence. The linked-token backend (tier 2b) is designed with a store
  schema in docs/auth.md; implementation is deferred (M5b). Tests: the verifier
  against a fake issuer with real RS256 signing (valid/expired/wrong-audience/
  wrong-issuer/absent-claim), DWD per-user impersonation and caching, bind-address
  rules, the metadata document, and a full HTTP integration test (401 without a
  token, wrong-audience rejected, and an authenticated `health` call over the
  streamable transport reporting resource-server mode). New dependencies:
  `github.com/coreos/go-oidc/v3` (+ `github.com/go-jose/go-jose/v4`) for OIDC
  discovery / JWKS / JWT verification, and `golang.org/x/oauth2/google` (+
  `/jwt`) for domain-wide-delegation service-account JWT signing — both foreseen
  in PLAN.md; hand-rolling JWT/JWKS crypto is inadvisable. docs/auth.md added.

- **M4 — Directory reads (Admin SDK).** Six read-only Directory tools:
  `directory_users_search`, `directory_user_get`, `directory_groups_search`,
  `directory_group_members`, `directory_roles_list`, and
  `directory_role_assignments`, all against `customer=my_customer` with `fields`
  projection and `nextPageToken` paging. Registered only behind a new `--admin`
  switch (`GWS_MCP_ADMIN`) — a registration switch, not a gate — which also adds
  the `admin.directory.{user,group,group.member,rolemanagement}.readonly`
  scopes, so consumer accounts keep a lean tool list and never consent to admin
  scopes. In classic-delegated mode these act as the signed-in admin user;
  Google enforces the caller's admin privileges (the SA-with-admin-role vs DWD
  question is deferred to the resource-server/application tiers). health now
  reports `admin`. `BaseDirectory` added. Recording-mock tests cover each tool,
  query wiring, not-found handling, and that the tools stay unregistered without
  `--admin`. No new dependencies.

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
