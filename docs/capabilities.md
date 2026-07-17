# Capabilities

The tool inventory, grouped by milestone. Legend: 🟢 read (always on) ·
🟡 write (`--allow-writes`) · 🔴 send/irreversible (`--allow-sends`).

Every tool acts as a real Google identity and Google enforces authorization —
this server adds no permission model of its own. In classic-delegated mode that
identity is the signed-in user, and all Gmail tools target `/users/me`.

## Operational

| Tool | Kind | Description |
| --- | --- | --- |
| `health` | 🟢 | Server name, version, transport, mode, gate state, and which `GWS_*` variables are set (booleans only). Makes no Google call. |

## Gmail — reads (M1)

Scope: `https://www.googleapis.com/auth/gmail.readonly`.

| Tool | Kind | Description |
| --- | --- | --- |
| `get_profile` | 🟢 | The signed-in user's Gmail profile: email address, total message/thread counts. |
| `list_labels` | 🟢 | System and user labels with their ids (for use as `labelIds` filters). |
| `list_messages` | 🟢 | List messages (optionally filtered by a Gmail query and/or label ids). Returns message + thread ids; page with `nextPageToken`. |
| `search_messages` | 🟢 | Same as `list_messages` but with a required Gmail query (`from:`, `is:unread`, `has:attachment`, `newer_than:`, …). |
| `get_message` | 🟢 | One message by id. `metadata` (default): common headers + snippet. `full`: adds the decoded plain-text body (capped at 100 KiB). |

### Projection and paging

- Every list/get uses Gmail's `fields` partial-response parameter to keep only
  the fields a tool surfaces, minimizing the PII placed in model context.
- Message listing returns a single bounded page (`maxResults` 1–100, default 25)
  and exposes `nextPageToken` for caller-driven continuation — mailboxes are
  large, so the client never auto-walks every page.
- Gmail is thread-centric: message summaries carry `threadId` as a first-class
  field.

## Later milestones

Calendar + Drive reads, gated writes/sends, Directory (Admin SDK), governance
(Reports/audit), the powerful-delegated and powerful-application tiers, and the
multi-user resource-server mode land in subsequent milestones.
