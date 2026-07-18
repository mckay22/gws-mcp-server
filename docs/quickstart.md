# Quickstart

Everything here is testable for **$0**. Three setups, matching the three tiers.

## A. Classic-delegated on a free `@gmail.com` (fastest)

Acts as **you**. No Workspace, no domain, no billing.

1. **Create a GCP project** at <https://console.cloud.google.com> (free, no card).
2. **Enable the APIs** you'll use (APIs & Services → Library): Gmail, Google
   Calendar, Google Drive. (People, Tasks, Chat, Meet only if you use
   `--powerful`.)
3. **Configure the OAuth consent screen** (APIs & Services → OAuth consent
   screen): User type **External**, publishing status **Testing**, and add your
   own `@gmail.com` as a **test user**. In Testing mode no app verification is
   needed for your own account. (Note: Testing-mode refresh tokens expire after
   ~7 days — you re-consent weekly, which is fine for development.)
4. **Create an OAuth client** (APIs & Services → Credentials → Create credentials
   → OAuth client ID): application type **Desktop app**. Copy the **client ID**
   and **client secret**.
5. **Run:**
   ```sh
   export GWS_CLIENT_ID="…apps.googleusercontent.com"
   export GWS_CLIENT_SECRET="…"
   ./gws-mcp-server
   ```
   On the first tool call the server prints an authorization URL to **stderr**
   (and opens your browser). Approve; it redirects to a loopback address and
   holds the token in memory.

Add `--allow-writes` and/or `--allow-sends` to move gated tools from dry-run
previews to real actions. Add `--powerful` for the end-user tools.

## B. Directory / admin on Cloud Identity Free

Cloud Identity Free gives you a directory (users, groups, admin roles) for **$0**
— it needs a **verified domain** you own (a spare domain, ~10 €/yr if you don't
have one). It has **no** Gmail/Drive/Calendar (no Workspace services), so
user-data tools will 403; the directory/governance tools work.

1. Sign up for **Cloud Identity Free** and verify your domain.
2. As an admin, do setup **A** in the Cloud Identity account's GCP project, but
   also enable the **Admin SDK API**, and on the OAuth consent screen add the
   `admin.directory.*.readonly` scopes (and the read-write ones if you'll use
   `--allow-writes`).
3. **Run with `--admin`** as the admin user:
   ```sh
   export GWS_CLIENT_ID=… GWS_CLIENT_SECRET=…
   ./gws-mcp-server --admin              # add --allow-writes for user/group lifecycle
   ```
   Google enforces your admin privileges on every directory call.

For the full Gmail/Drive/Calendar surface under an org, add a **Workspace
Business trial** (14 days) on top of Cloud Identity — **turn off auto-renew on
day 1**. Nothing in this project ever requires a paid SKU to build.

## C. Resource-server mode (multi-user) with domain-wide delegation

For a shared deployment: validate each request's bearer token and act as the
mapped user via a DWD service account.

1. **Create a service account** in your GCP project and generate a **JSON key**.
2. **Grant it domain-wide delegation** (Workspace Admin console → Security → API
   controls → Domain-wide delegation): add the SA's **client ID** and the
   **exact scope list** it may mint tokens for. Keep this list minimal — it is
   the hard authorization boundary.
3. **Point the server at your OIDC issuer** and the SA key:
   ```sh
   export GWS_AUDIENCE="api://gws-mcp"                    # audience tokens must carry
   export GWS_ISSUERS="https://keycloak.example/realms/main"  # trusted issuer(s)
   export GWS_DWD_SA_KEY="/run/secrets/dwd-sa.json"
   # optional: export GWS_SUBJECT_CLAIM="email"           # token claim → Google user
   ./gws-mcp-server --http :8080
   ```
   Clients discover the audience and issuers at
   `/.well-known/oauth-protected-resource` (RFC 9728). Because every request is
   authenticated, a non-loopback bind is allowed.

**Run this behind an authenticating gateway**: the gateway owns role-based tool
exposure and deny policies; this server stays policy-free and lets Google enforce
each impersonated user's rights. Tool annotations (see
[capabilities](capabilities.md#tool-annotations)) give such a gateway a
per-tool read-only/destructive signal to write policy against.

### Application tier (`--app-only`)

For tools that act on an **explicit** `user` target (and bulk directory
lifecycle), give the application tier its **own** service account — it **must**
be a different key from the resource-server one (enforced at startup):

```sh
export GWS_APP_SA_KEY="/run/secrets/app-sa.json"      # separate from GWS_DWD_SA_KEY
export GWS_APP_ADMIN_SUBJECT="admin@your-domain"      # for the bulk directory tools
./gws-mcp-server --http :8080 --app-only --allow-writes
```

Every applied `app_*` mutation is logged with the requesting actor.

## Docker

```sh
docker build -t gws-mcp-server .
docker run --rm -p 8080:8080 \
  -e GWS_AUDIENCE=api://gws-mcp \
  -e GWS_ISSUERS=https://keycloak.example/realms/main \
  -e GWS_DWD_SA_KEY=/keys/dwd-sa.json \
  -v /path/to/dwd-sa.json:/keys/dwd-sa.json:ro \
  gws-mcp-server --http 0.0.0.0:8080
```

Note the explicit `0.0.0.0`. A bare `--http :8080` binds loopback *inside* the
container, so `-p 8080:8080` would forward to nothing — a host-published port
needs the server listening on all interfaces. This is allowed only because
resource-server mode authenticates every request; without it the server refuses
a non-loopback bind outright.

The image is a scratch-based static binary (+ CA certs) running as an
unprivileged uid. It is intended for resource-server mode; classic-delegated
sign-in needs an interactive browser, which is awkward in a container.
