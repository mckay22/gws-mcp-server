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

## Directory — reads (M4, Admin SDK)

Registered only with `--admin` (or `GWS_MCP_ADMIN=true`), which also requests the
`admin.directory.*.readonly` scopes. These need the signed-in user to hold a
Workspace/Cloud Identity admin role; consumer `@gmail.com` accounts leave the
switch off. Google enforces the caller's admin privileges on every call.

| Tool | Kind | Description |
| --- | --- | --- |
| `directory_users_search` | 🟢 | Search/list directory users (Admin SDK query syntax). Compact summaries; `nextPageToken`. |
| `directory_user_get` | 🟢 | One user by email/id: aliases, org unit, admin flags, 2SV enrollment. |
| `directory_groups_search` | 🟢 | Search/list groups, optionally those a given user belongs to. |
| `directory_group_members` | 🟢 | A group's members with role (OWNER/MANAGER/MEMBER) and type. |
| `directory_roles_list` | 🟢 | Admin roles (system + custom), flagging super-admin. |
| `directory_role_assignments` | 🟢 | Who holds which admin role, filterable by user or role id. |

## Governance (M6, Admin SDK)

Also behind `--admin`. Reads need reporting/security/licensing admin privileges;
the Directory write tools additionally honor `--allow-writes` (dry-run preview
until open). Edition-gated audit event types and license queries surface the
Google error cleanly.

| Tool | Kind | Description |
| --- | --- | --- |
| `audit_activities` | 🟢 | Admin Reports audit log for an application (login/admin/drive/token/…): who did what, when, from where. |
| `user_connected_apps` | 🟢 | The third-party OAuth apps a user has granted access to, with scopes — the connected-app/consent audit. |
| `license_assignments` | 🟢 | License assignments for a product/SKU across users (Enterprise License Manager). |
| `directory_user_create` | 🟡 | Create a user (password **redacted** in the preview). |
| `directory_user_update` | 🟡 | Patch a user's name / org unit. |
| `directory_user_suspend` | 🟡 | Suspend or un-suspend a user. |
| `directory_group_create` | 🟡 | Create a group. |
| `directory_group_add_member` | 🟡 | Add a member with a role. |
| `directory_group_remove_member` | 🟡 | Remove a member. |

## Powerful-delegated tier (M7)

Behind `--powerful` (or `GWS_MCP_POWERFUL=true`) — a registration switch; each
tool still honors the write/send gates. Chat and Meet are Workspace-only and
error cleanly on consumer accounts. Extra scopes (`gmail.settings.basic`,
`tasks[.readonly]`, `contacts.readonly`, `chat.*`, `meetings.space.readonly`) are
requested only when the switch is on.

| Tool | Kind | Description |
| --- | --- | --- |
| `gmail_get_vacation` | 🟢 | The vacation responder (out-of-office) settings. |
| `gmail_set_vacation` | 🟡 | Enable/disable the vacation responder. |
| `gmail_list_filters` | 🟢 | Gmail filters (the inbox-rules analog). |
| `gmail_list_send_as` | 🟢 | Send-as addresses / aliases. |
| `tasks_list_tasklists` | 🟢 | The user's task lists. |
| `tasks_list` | 🟢 | Tasks in a list. |
| `tasks_create` | 🟡 | Create a task. |
| `tasks_complete` | 🟡 | Mark a task completed. |
| `people_search_contacts` | 🟢 | Search personal contacts (People API). |
| `chat_list_spaces` | 🟢 | Google Chat spaces the user is in. |
| `chat_list_messages` | 🟢 | Messages in a Chat space. |
| `chat_send_message` | 🔴 | Post a message to a Chat space. |
| `meet_conference_records` | 🟢 | Meet conference records (→ recordings/transcripts). |
| `drive_shared_with_me` | 🟢 | Files others have shared with the user. |

## Resource-server mode (M5)

The same tool surface is also served over HTTP in multi-user mode (`--http
<addr>` with `GWS_AUDIENCE` set): each request's bearer token is verified against
a trusted OIDC issuer, mapped to a Google user, and impersonated via domain-wide
delegation. See [auth.md](auth.md) for the identity model. The tool inventory is
identical to stdio mode; only the identity backend changes.

## Later milestones

Governance (Reports/audit, Directory writes), and the powerful-delegated and
powerful-application tiers land in subsequent milestones.
