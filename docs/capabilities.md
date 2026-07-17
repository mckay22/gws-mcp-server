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

## Calendar — reads (M2)

Scope: `https://www.googleapis.com/auth/calendar.readonly`.

| Tool | Kind | Description |
| --- | --- | --- |
| `list_calendars` | 🟢 | The user's calendar list with ids, access role, and time zone. |
| `list_events` | 🟢 | Events in a calendar within a time window (default primary calendar, next 30 days), expanded to single instances and ordered by start time. One page; `nextPageToken`. |
| `get_event` | 🟢 | One event by id, including description and attendee RSVP states. |
| `freebusy_query` | 🟢 | Busy intervals for one or more calendars in a window (default primary, next 7 days) — availability without event details. |

Time bounds are RFC3339; blank values default, malformed values are rejected.

## Drive — reads (M2)

Scope: `https://www.googleapis.com/auth/drive.readonly`.

| Tool | Kind | Description |
| --- | --- | --- |
| `list_files` | 🟢 | List (recent-first) or search files with Drive's query syntax. One page of metadata; `nextPageToken`; optional shared-drive span. Trash excluded by default. |
| `get_file_content` | 🟢 | A file's text content by id. Google Docs/Sheets/Slides are exported to text/CSV; other files download directly; binary-only files are rejected. Capped at 200 KiB. |

## Later milestones

Gated writes/sends, Directory (Admin SDK), governance (Reports/audit), the
powerful-delegated and powerful-application tiers, and the multi-user
resource-server mode land in subsequent milestones.
