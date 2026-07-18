# Identity & authentication

This server never builds its own permission model. It acts as a real Google
identity and lets Google enforce authorization on every call. There are three
identity tiers; two are implemented today.

## Tier 1 — classic-delegated (default, stdio)

You sign in with your own Google account through the OAuth installed-app flow
(loopback redirect + PKCE, `internal/googleauth`). The server acts as you;
Google evaluates *your* rights on every call. Works with a free consumer
`@gmail.com` — no Workspace, no domain, no billing. The token is held in memory
for the process's lifetime and refreshed transparently. Sign-in is lazy: nothing
happens at startup, and the authorization URL is printed on the first tool call.

Config: `GWS_CLIENT_ID`, `GWS_CLIENT_SECRET` (a GCP "Desktop app" OAuth client
you create in your own project).

## Tier 2 — resource-server mode (opt-in, HTTP, multi-user)

Selected by serving over `--http <addr>` with `GWS_AUDIENCE` set. Every request
must carry a bearer token minted for this server (`aud` = `GWS_AUDIENCE`) by a
trusted OIDC issuer. The flow per request:

1. **Verify** the token (`internal/oidcauth`): signature against the issuer's
   JWKS, issuer in the `GWS_ISSUERS` allowlist, audience, and expiry. The
   verifier is **issuer-agnostic** — any OIDC-compliant IdP works (Keycloak,
   Microsoft Entra ID, Google itself); each issuer's metadata and JWKS are
   discovered at startup.
2. **Map** the verified token to a Google user via a configurable claim
   (`GWS_SUBJECT_CLAIM`, default `email`).
3. **Impersonate** that user through the **DWD backend** (`internal/googleauth`
   `DWD`): mint a service-account-signed JWT with `sub=<that user>` (domain-wide
   delegation) and exchange it for an access token, cached per user. This is the
   Google analog of the Microsoft On-Behalf-Of exchange — Google has no OBO for
   Workspace user data, so DWD is the substitute.

Identity always comes from the validated token — there is **no** caller-supplied
"act as X" parameter in this tier. Google still enforces the impersonated user's
own authorization, because the minted token *is* that user.

Config: `GWS_AUDIENCE`, `GWS_ISSUERS` (comma-separated issuer URLs),
`GWS_DWD_SA_KEY` (path to the DWD service-account JSON key), optional
`GWS_SUBJECT_CLAIM`.

The server publishes RFC 9728 Protected Resource Metadata at
`/.well-known/oauth-protected-resource` so a client can discover the audience and
authorization servers.

### DWD key security

The DWD service-account key is a **domain credential**: within the scopes granted
on its DWD authorization, it can mint a token as *any* user in the domain. So:

- Keep the DWD-granted scope list minimal (it is the hard boundary).
- Provide the key by **path** at runtime, stored outside the repo; it is never
  logged and its presence is reported as a boolean only.
- This backend is recommended **only behind an authenticating gateway** in shared
  deployments — the gateway owns role-based tool exposure and deny policies; this
  server stays policy-free.
- A non-loopback `--http` bind is refused unless resource-server mode is
  configured, because only then is every request authenticated.

## Tier 2b — linked-token backend (designed; implementation deferred)

An alternative to DWD with **no god credential**, and the only option for
consumer accounts (Workforce Identity Federation does not cover Gmail/Drive/
Calendar user data). Design:

- A one-time, per-user **consent flow** runs the installed-app OAuth flow for
  each user and stores their refresh token.
- Per request, the verified external subject is mapped to their stored Google
  refresh token, which mints access tokens as that user.
- **Store schema** (encrypted at rest, key via env — e.g. NaCl secretbox / age):

  | field | meaning |
  | --- | --- |
  | `subject` | the verified external token subject (map key) |
  | `google_user` | the linked Google account email |
  | `refresh_token` | encrypted; never logged or returned |
  | `scopes` | the scopes consented at linking |
  | `linked_at` | timestamp of the consent |

  The store is per-tier and never readable by another tier.

Trade-off: no domain credential, but it costs state management and a linking
flow. The default deployment implements DWD first (org deployments); linking is
deferred until a consumer multi-user need appears (tracked as M5b).

## Tier 3 — powerful-application (opt-in, explicit)

Selected by `--app-only` (`GWS_MCP_APP_ONLY`). It has its **own** service account
(`GWS_APP_SA_KEY`) with its own DWD grant, which **must differ** from the
resource-server backend's key — enforced at startup, so a leaked resource-server
credential cannot escalate to the application tier.

The `app_*` tools take a required `user` parameter and act on that principal via
the app SA's domain-wide delegation (reusing the DWD backend by injecting the
target user into the call context). Per-user tools (mailbox, calendar, Drive,
vacation) impersonate their own target; the bulk Directory tools impersonate a
configured admin (`GWS_APP_ADMIN_SUBJECT`) because directory admin operations
need admin rights. Both gates still apply.

**Requesting-actor logging.** Google's audit attributes a DWD action to the
*impersonated* user, so every applied application-tier mutation is additionally
logged here with the requesting actor — the verified caller (resource-server
mode) or `local` (stdio). That log is where the real requester is recorded.

## Permission model (unchanged principle)

- Tier 1: Google enforces the signed-in user's rights.
- Tier 2: Google enforces the impersonated user's rights.
- Role-based tool menus, deny-by-default policies, and per-user tool lists belong
  to an **external authorization layer in front** (an MCP gateway). This server
  stays policy-free.
