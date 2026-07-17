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
)

// requiredScopes returns the OAuth scopes the currently-registered tools need.
// Reads are always on, so their scopes are always included; gated write/send
// tools reuse the same read scopes for previews and only need broader scopes
// once the gates open (handled as those milestones land).
func requiredScopes(_ config.Config) []string {
	return []string{
		scopeGmailReadonly,
		scopeCalendarReadonly,
		scopeDriveReadonly,
	}
}
