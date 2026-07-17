package main

import "github.com/mckay22/gws-mcp-server/internal/config"

// OAuth scopes requested at sign-in. Each covers a group of read-only tools; as
// later milestones add tool groups, their scopes join the union built by
// requiredScopes. The classic-delegated flow consents to exactly this set, and
// Google enforces it on every call — least privilege lives in this list, not in
// a parallel permission model.
const (
	// scopeGmailReadonly covers the M1 Gmail read tools (profile, labels,
	// message list/search/get).
	scopeGmailReadonly = "https://www.googleapis.com/auth/gmail.readonly"

	// scopeCalendarReadonly covers the M2 Calendar read tools (calendars,
	// events, free/busy).
	scopeCalendarReadonly = "https://www.googleapis.com/auth/calendar.readonly"

	// scopeDriveReadonly covers the M2 Drive read tools (list/search, file
	// content download/export).
	scopeDriveReadonly = "https://www.googleapis.com/auth/drive.readonly"

	// Write-gated scopes (M3), requested only when --allow-writes is on.
	// gmail.modify covers draft creation and label changes; calendar.events
	// covers event mutations; drive (full) covers uploads and — with the send
	// gate — sharing existing files.
	scopeGmailModify    = "https://www.googleapis.com/auth/gmail.modify"
	scopeCalendarEvents = "https://www.googleapis.com/auth/calendar.events"
	scopeDrive          = "https://www.googleapis.com/auth/drive"

	// Send-gated scope (M3), requested only when --allow-sends is on: sending
	// mail as the user. (Calendar/Drive send-class actions reuse the write-gated
	// scopes above; the send gate governs whether they are invoked.)
	scopeGmailSend = "https://www.googleapis.com/auth/gmail.send"

	// Admin SDK Directory read scopes (M4), requested only when --admin is on.
	scopeAdminUserReadonly     = "https://www.googleapis.com/auth/admin.directory.user.readonly"
	scopeAdminGroupReadonly    = "https://www.googleapis.com/auth/admin.directory.group.readonly"
	scopeAdminGroupMemberRO    = "https://www.googleapis.com/auth/admin.directory.group.member.readonly"
	scopeAdminRoleMgmtReadonly = "https://www.googleapis.com/auth/admin.directory.rolemanagement.readonly"

	// Governance read scopes (M6), requested when --admin is on: audit reports,
	// a user's connected-app tokens (user.security), and license assignments.
	scopeReportsAudit  = "https://www.googleapis.com/auth/admin.reports.audit.readonly"
	scopeUserSecurity  = "https://www.googleapis.com/auth/admin.directory.user.security"
	scopeAppsLicensing = "https://www.googleapis.com/auth/apps.licensing"

	// Admin write scopes (M6), requested only when --admin AND --allow-writes are
	// both on: the read-write directory scopes for user/group lifecycle.
	scopeAdminUser        = "https://www.googleapis.com/auth/admin.directory.user"
	scopeAdminGroup       = "https://www.googleapis.com/auth/admin.directory.group"
	scopeAdminGroupMember = "https://www.googleapis.com/auth/admin.directory.group.member"

	// Powerful-delegated scopes (M7), requested only when --powerful is on.
	// gmail.settings.basic covers reading filters/send-as and reading/writing the
	// vacation responder. tasks.readonly / tasks split reads from writes.
	// contacts.readonly covers People contact search. Chat read scopes cover
	// spaces/messages; chat.messages.create (send) is added under --allow-sends.
	// meetings.space.readonly covers Meet conference records.
	scopeGmailSettingsBasic = "https://www.googleapis.com/auth/gmail.settings.basic"
	scopeTasksReadonly      = "https://www.googleapis.com/auth/tasks.readonly"
	scopeTasks              = "https://www.googleapis.com/auth/tasks"
	scopeContactsReadonly   = "https://www.googleapis.com/auth/contacts.readonly"
	scopeChatSpacesRO       = "https://www.googleapis.com/auth/chat.spaces.readonly"
	scopeChatMessagesRO     = "https://www.googleapis.com/auth/chat.messages.readonly"
	scopeChatMessages       = "https://www.googleapis.com/auth/chat.messages.create"
	scopeMeetReadonly       = "https://www.googleapis.com/auth/meetings.space.readonly"
)

// requiredScopes returns the OAuth scopes the currently-enabled tools need.
// Reads are always on, so their scopes are always included. Write/send scopes
// are requested ONLY when the corresponding gate is open, so a read-only
// deployment never consents to a mutating scope — least privilege lives here,
// enforced by Google on every call.
func requiredScopes(cfg config.Config) []string {
	scopes := []string{
		scopeGmailReadonly,
		scopeCalendarReadonly,
		scopeDriveReadonly,
	}
	if cfg.AllowWrites {
		scopes = append(scopes, scopeGmailModify, scopeCalendarEvents, scopeDrive)
	}
	if cfg.AllowSends {
		scopes = append(scopes, scopeGmailSend)
	}
	if cfg.Admin {
		scopes = append(scopes,
			scopeAdminUserReadonly,
			scopeAdminGroupReadonly,
			scopeAdminGroupMemberRO,
			scopeAdminRoleMgmtReadonly,
			// Governance reads (M6).
			scopeReportsAudit,
			scopeUserSecurity,
			scopeAppsLicensing,
		)
		if cfg.AllowWrites {
			// Directory write lifecycle (M6): the read-write directory scopes,
			// requested only when both the admin switch and the write gate are on.
			scopes = append(scopes, scopeAdminUser, scopeAdminGroup, scopeAdminGroupMember)
		}
	}
	if cfg.Powerful {
		scopes = append(scopes,
			scopeGmailSettingsBasic,
			scopeTasksReadonly,
			scopeContactsReadonly,
			scopeChatSpacesRO,
			scopeChatMessagesRO,
			scopeMeetReadonly,
		)
		if cfg.AllowWrites {
			scopes = append(scopes, scopeTasks) // task create/complete
		}
		if cfg.AllowSends {
			scopes = append(scopes, scopeChatMessages) // Chat message send
		}
	}
	return scopes
}
