# Capabilities

The tool inventory, grouped by milestone. Legend: 🟢 read (always on) ·
🟡 write (`--allow-writes`) · 🔴 send/irreversible (`--allow-sends`).

Every tool acts as a real Google identity and Google enforces authorization —
this server adds no permission model of its own. In classic-delegated mode that
identity is the signed-in user, and all Gmail tools target `/users/me`.

## Naming

Every tool is `<service>_<verb>[_<object>]`: the service prefix first, so names
stay unambiguous when several MCP servers are connected at once, then what the
tool does. `health` is the one exception — it describes the server, not a Google
service.

Services are `gmail_`, `calendar_`, `drive_`, `directory_` (Admin SDK Directory),
`admin_` (Admin SDK Reports/Licensing), `tasks_`, `people_`, `chat_`, `meet_`, and
`app_` (the powerful-application tier, which acts on an explicit `user` target).

## Tool annotations

Every tool advertises MCP tool annotations, so a client — or a policy layer in
front of one — can judge a call without pattern-matching on tool names. The
mapping from the legend above:

| Legend | `readOnlyHint` | `destructiveHint` | `idempotentHint` | Applies to |
| --- | --- | --- | --- | --- |
| 🟢 read | `true` | — | — | the 35 read tools (incl. `health`) |
| 🟡 / 🔴 additive | `false` | `false` | `false` | creates something new: drafts, events, uploads, group members, sent mail |
| 🟡 / 🔴 overwrite or remove | `false` | `true` | `true` | patches a resource in place, cancels, removes, suspends |

`openWorldHint` is `true` for every Google-backed tool and `false` only for
`health`, which makes no Google call.

Two things worth knowing when consuming these:

- **They describe, they do not enforce.** The enforcement points are the write
  and send gates and Google's own authorization. The MCP spec is explicit that a
  client should treat annotations from an untrusted server as unverified claims.
- **Irreversible is not the same as destructive.** `destructiveHint` follows the
  spec's definition — deleting or overwriting, versus adding — so `gmail_send`
  is `destructiveHint: false`: it creates a message and destroys nothing. Its
  irreversibility is carried by the separate send gate (🔴), not by this hint. A
  policy layer that cares about egress should key on the gate, not on
  `destructiveHint` alone.

## Operational

| Tool | Kind | Description |
| --- | --- | --- |
| `health` | 🟢 | Server name, version, transport, mode, gate state, and which `GWS_*` variables are set (booleans only). Makes no Google call. |

## Gmail — reads (M1)

Scope: `https://www.googleapis.com/auth/gmail.readonly`.

| Tool | Kind | Description |
| --- | --- | --- |
| `gmail_get_profile` | 🟢 | The signed-in user's Gmail profile: email address, total message/thread counts. |
| `gmail_list_labels` | 🟢 | System and user labels with their ids (for use as `labelIds` filters). |
| `gmail_list_messages` | 🟢 | List **or search** messages: pass a Gmail query (`from:`, `is:unread`, `has:attachment`, `newer_than:`, …) and/or label ids, or omit both to list. Returns message + thread ids; page with `nextPageToken`. |
| `gmail_get_message` | 🟢 | One message by id. `metadata` (default): common headers + snippet. `full`: adds the decoded body (capped at 100 KiB) — the plain-text part, or the HTML reduced to text when the message carries HTML only, flagged with `bodyFromHtml`. |

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
| `calendar_list_calendars` | 🟢 | The user's calendar list with ids, access role, and time zone. |
| `calendar_list_events` | 🟢 | Events in a calendar within a time window (default primary calendar, next 30 days), expanded to single instances and ordered by start time. One page; `nextPageToken`. |
| `calendar_get_event` | 🟢 | One event by id, including description and attendee RSVP states. |
| `calendar_freebusy` | 🟢 | Busy intervals for one or more calendars in a window (default primary, next 7 days) — availability without event details. |

Time bounds are RFC3339; blank values default, malformed values are rejected.

## Drive — reads (M2)

Scope: `https://www.googleapis.com/auth/drive.readonly`.

| Tool | Kind | Description |
| --- | --- | --- |
| `drive_list_files` | 🟢 | List (recent-first) or search files with Drive's query syntax. One page of metadata; `nextPageToken`; optional shared-drive span. Trash excluded by default. |
| `drive_get_file_content` | 🟢 | A file's text content by id. Google Docs/Sheets/Slides are exported to text/CSV; other files download directly; binary-only files are rejected. Capped at 200 KiB. |

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
| `gmail_modify_labels` | 🟡 | Add/remove message labels — the mechanism behind read/unread, archive, star. |
| `gmail_send` | 🔴 | Send a new message as the user. Preview shows full To/Cc/Subject/body. |
| `gmail_reply` | 🔴 | Send a reply within a thread. |
| `calendar_create_appointment` | 🟡 | Create an event with **no attendees** (nobody emailed). |
| `calendar_create_event_with_attendees` | 🔴 | Create an event and email invitations (`sendUpdates=all`). |
| `calendar_update_event` | 🔴 | Patch an event and notify attendees. |
| `calendar_cancel_event` | 🔴 | Delete/cancel an event and notify attendees. |
| `calendar_respond_to_event` | 🔴 | Set the user's RSVP and notify the organizer. |
| `drive_upload_file` | 🟡 | Create a Drive file from text content (multipart upload). |
| `drive_share_file` | 🔴 | Grant a permission on a file/folder — egress. |

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
| `directory_search_users` | 🟢 | Search/list directory users (Admin SDK query syntax). Compact summaries; `nextPageToken`. |
| `directory_get_user` | 🟢 | One user by email/id: aliases, org unit, admin flags, 2SV enrollment. |
| `directory_search_groups` | 🟢 | Search/list groups, optionally those a given user belongs to. |
| `directory_list_group_members` | 🟢 | A group's members with role (OWNER/MANAGER/MEMBER) and type. |
| `directory_list_roles` | 🟢 | Admin roles (system + custom), flagging super-admin. |
| `directory_list_role_assignments` | 🟢 | Who holds which admin role, filterable by user or role id. |

## Governance (M6, Admin SDK)

Also behind `--admin`. Reads need reporting/security/licensing admin privileges;
the Directory write tools additionally honor `--allow-writes` (dry-run preview
until open). Edition-gated audit event types and license queries surface the
Google error cleanly.

| Tool | Kind | Description |
| --- | --- | --- |
| `admin_list_audit_activities` | 🟢 | Admin Reports audit log for an application (login/admin/drive/token/…): who did what, when, from where. |
| `admin_list_connected_apps` | 🟢 | The third-party OAuth apps a user has granted access to, with scopes — the connected-app/consent audit. |
| `admin_list_license_assignments` | 🟢 | License assignments for a product/SKU across users (Enterprise License Manager). |
| `directory_create_user` | 🟡 | Create a user (password **redacted** in the preview). |
| `directory_update_user` | 🟡 | Patch a user's name / org unit. |
| `directory_suspend_user` | 🟡 | Suspend or un-suspend a user. |
| `directory_create_group` | 🟡 | Create a group. |
| `directory_add_group_member` | 🟡 | Add a member with a role. |
| `directory_remove_group_member` | 🟡 | Remove a member. |

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
| `meet_list_conference_records` | 🟢 | Meet conference records (→ recordings/transcripts). |
| `drive_shared_with_me` | 🟢 | Files others have shared with the user. |

## Powerful-application tier (M8)

Behind `--app-only` (or `GWS_MCP_APP_ONLY=true`), which requires its **own**
service-account key (`GWS_APP_SA_KEY`) that **must differ** from the
resource-server DWD key — enforced at startup, so a leaked resource-server key
cannot escalate. Each tool takes a required `user` target and acts on that
principal via the app SA's domain-wide delegation; the tools still honor the
write/send gates. **Every applied mutation is logged with the requesting actor**
(the verified caller in resource-server mode, or `local` on stdio).

| Tool | Kind | Description |
| --- | --- | --- |
| `app_list_messages` | 🟢 | List a target user's messages. |
| `app_get_message` | 🟢 | Get a target user's message (`full` adds the body). |
| `app_send_mail` | 🔴 | Send mail **as** a target user. |
| `app_list_events` | 🟢 | List a target user's calendar events. |
| `app_list_files` | 🟢 | List a target user's Drive files. |
| `app_set_vacation` | 🟡 | Set a target user's vacation responder. |
| `app_bulk_user_suspend` | 🟡 | Suspend/un-suspend many users — per-item outcomes, duplicate targets rejected. |
| `app_bulk_group_add_members` | 🟡 | Add many members to a group — per-item outcomes, duplicates rejected. |

The bulk Directory tools impersonate the configured admin
(`GWS_APP_ADMIN_SUBJECT`); the per-user tools impersonate their own target.

## Resource-server mode (M5)

The same tool surface is also served over HTTP in multi-user mode (`--http
<addr>` with `GWS_AUDIENCE` set): each request's bearer token is verified against
a trusted OIDC issuer, mapped to a Google user, and impersonated via domain-wide
delegation. See [auth.md](auth.md) for the identity model. The tool inventory is
identical to stdio mode; only the identity backend changes.

