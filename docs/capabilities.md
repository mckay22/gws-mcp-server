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

## Gated writes and sends (M3)

Two independent gates. `--allow-writes` (or `GWS_MCP_ALLOW_WRITES=true`) enables
reversible mutations 🟡; `--allow-sends` (or `GWS_MCP_ALLOW_SENDS=true`) enables
irreversible/egress actions 🔴. **The write gate never implies the send gate.**
With a gate closed, its tools return a **dry-run preview** — the exact method,
URL, and body — and make no Google call. Extra OAuth scopes
(`gmail.modify`/`gmail.send`, `calendar.events`, `drive`) are requested only when
the matching gate is open.

| Tool | Kind | Description |
| --- | --- | --- |
| `gmail_create_draft` | 🟡 | Save a draft (not sent). |
| `gmail_modify` | 🟡 | Add/remove message labels — the mechanism behind read/unread, archive, star. |
| `gmail_send` | 🔴 | Send a new message as the user. Preview shows full To/Cc/Subject/body. |
| `gmail_reply` | 🔴 | Send a reply within a thread. |
| `create_appointment` | 🟡 | Create an event with **no attendees** (nobody emailed). |
| `create_event_with_attendees` | 🔴 | Create an event and email invitations (`sendUpdates=all`). |
| `update_event` | 🔴 | Patch an event and notify attendees. |
| `cancel_event` | 🔴 | Delete/cancel an event and notify attendees. |
| `respond_to_event` | 🔴 | Set the user's RSVP and notify the organizer. |
| `upload_file` | 🟡 | Create a Drive file from text content (multipart upload). |
| `share_file` | 🔴 | Grant a permission on a file/folder — egress. |

The Calendar **attendee split is the gate split**: an appointment with no
attendees notifies no one (write gate); anything that emails attendees or the
organizer is send-gated.

## Later milestones

Directory (Admin SDK), governance (Reports/audit), the powerful-delegated and
powerful-application tiers, and the multi-user resource-server mode land in
subsequent milestones.
