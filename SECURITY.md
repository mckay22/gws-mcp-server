# Security policy

`gws-mcp-server` exposes Google Workspace to AI assistants. Its security posture
follows one rule: **Google stays the authority — the server builds no parallel
permission model.** Everything below serves that rule.

## Delegated by default

The server acts **as a real Google identity** — the signed-in user
(classic-delegated) or the verified caller (resource-server) — using delegated
OAuth scopes. Google therefore re-evaluates that identity's rights on **every**
call; the server never decides who may do what.

- There is **no "act as X" tool argument** in the delegated tiers. Identity comes
  only from the token / signed-in user.
- The **powerful-application** tier (`--app-only`) *does* act on explicit
  targets via domain-wide delegation — a deliberate, opt-in exception. It uses a
  **separate service account** whose key must differ from the resource-server
  key (enforced at startup), and every applied mutation is logged with the
  requesting actor.

## Two independent safety gates

- **Read-only by default.** Mutations require `--allow-writes`; irreversible or
  egress actions (mail send/reply, calendar invites, Drive sharing, Chat send)
  require a **separate** `--allow-sends`. Opening the write gate never opens the
  send gate.
- Gated tools return a **dry-run preview** — the exact method, URL, and body —
  and make no Google call until their gate is open. Send previews show the full
  recipients and body so a human can see the message before it goes out.
- Sensitive values in previews are redacted (e.g. a new user's password) while
  the real value is still sent on apply.

## Least-privilege scopes

The OAuth scope set is assembled from what is actually enabled: read scopes are
always requested; write/send/admin scopes are added **only** when the matching
gate or registration switch is on. A read-only deployment never consents to a
mutating scope. For domain-wide delegation, the admin-granted scope list on the
delegation grant is the hard boundary — keep it minimal.

## Resource-server mode

The shared HTTP mode is an OAuth 2.1 resource server:

- **Validates every token**: signature against the issuer's JWKS, plus issuer
  (against an allowlist), audience (must name this server), and expiry. A token
  minted for any other audience is rejected.
- **No token pass-through.** The caller's token is minted *for this server*; the
  server verifies it, maps it to a Google user via a configured claim, and mints
  a **new** token via a domain-wide-delegation service account. The caller's
  token is never forwarded to Google.
- Publishes RFC 9728 Protected Resource Metadata at
  `/.well-known/oauth-protected-resource` and, when the audience is a URL with a
  path, at the path-appended location RFC 9728 §3.1 specifies. A 401 carries a
  `WWW-Authenticate` header pointing at it, so a client can discover where to get
  a token.
- Applies cross-origin protection to the MCP endpoint, rejecting cross-origin
  browser requests (defence in depth against DNS rebinding and CSRF).
- Refuses a **non-loopback** `--http` bind unless resource-server mode is
  configured (only then is every request authenticated). In practice `--http`
  requires that configuration outright — there is no unauthenticated HTTP mode,
  loopback or otherwise.

Run resource-server / application mode **behind an authenticating gateway**. That
gateway owns role-based tool exposure and deny-by-default policy; this server
stays policy-free.

## Credential handling

- Secrets are read from the **environment / files at runtime**, never from code,
  fixtures, or logs. Service-account keys are provided by **path**; their
  contents are never logged and their presence is reported as a boolean only.
- Tokens are held **in memory** (classic-delegated) or minted per request
  (resource-server / application); no part of a token or key is ever
  materialized into output.
- The repo is public: no real customer ids, domains, emails, keys, or tokens are
  ever committed. Test fixtures use `example.com` and placeholder ids.

## Reporting a vulnerability

Please open a private security advisory on the GitHub repository, or contact the
maintainer. Do not file a public issue for an unpatched vulnerability.
