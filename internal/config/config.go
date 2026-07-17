// Package config parses the gws-mcp-server runtime configuration from the
// environment. Credentials are read at runtime and never written to disk, logs,
// or tool output — only presence booleans are ever exposed. Auth itself is not
// wired up yet (M0 is scaffold; the installed-app OAuth flow lands in M1), so
// this package parses and reports but never requires anything.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Environment variables consumed by ConfigFromEnv.
const (
	// EnvClientID is the OAuth client id (GCP "Desktop app" client) the
	// classic-delegated tier signs in with. Users create their own client in
	// their own GCP project — there is no sane shared-client story for an
	// open-source server requesting sensitive scopes.
	EnvClientID = "GWS_CLIENT_ID"

	// EnvClientSecret is the client secret paired with EnvClientID. Google does
	// not treat installed-app client secrets as confidential, but this server
	// does anyway: the value is never logged or returned.
	EnvClientSecret = "GWS_CLIENT_SECRET"

	// EnvAllowWrites is the write gate. Unset or anything other than "true"
	// (case-insensitive) keeps the server read-only: mutating tools return a
	// dry-run preview instead of calling Google.
	EnvAllowWrites = "GWS_MCP_ALLOW_WRITES"

	// EnvAllowSends is the send gate — a SEPARATE, stricter switch than the
	// write gate because sending mail (and other egress, like sharing) is
	// irreversible or leaves the account. Unset or anything other than "true"
	// (case-insensitive) keeps send-class tools in dry-run: they return a
	// preview instead of calling Google. Opening the write gate does not open
	// this one.
	EnvAllowSends = "GWS_MCP_ALLOW_SENDS"
)

// ModeClassicDelegated is the default operating mode: you sign in with your own
// Google account and the server acts as you, with Google enforcing your rights
// on every call. The resource-server and powerful-application tiers land in
// later milestones (M5, M8).
const ModeClassicDelegated = "classic-delegated"

// Config holds everything needed to reach the Google APIs.
//
// M0 is a scaffold: ConfigFromEnv parses these values and reports what is
// present, but never requires them. The OAuth client credentials are consumed
// in M1.
type Config struct {
	ClientID string

	// ClientSecret pairs with ClientID for the installed-app OAuth flow. Its
	// value is never logged or returned — expose presence via Presence instead.
	ClientSecret string

	// AllowWrites is the write gate: when false (the default) every mutating
	// tool returns a dry-run preview instead of calling Google. It is set by
	// GWS_MCP_ALLOW_WRITES=true (case-insensitive) and carries no secret. The
	// --allow-writes flag can force it on at startup.
	AllowWrites bool

	// AllowSends is the send gate: a SEPARATE switch from AllowWrites because
	// send-class actions are irreversible. When false (the default) every
	// send-class tool returns a dry-run preview instead of calling Google, even
	// if AllowWrites is true. It is set by GWS_MCP_ALLOW_SENDS=true
	// (case-insensitive) and carries no secret.
	AllowSends bool
}

// ConfigFromEnv builds a Config from the GWS_* environment variables. It does
// not require any of them: because M0 is a scaffold and auth lands in M1, a
// missing variable is reported (as a presence boolean), not treated as an
// error.
func ConfigFromEnv() Config {
	return Config{
		ClientID:     strings.TrimSpace(os.Getenv(EnvClientID)),
		ClientSecret: os.Getenv(EnvClientSecret),
		AllowWrites:  boolFromEnv(EnvAllowWrites),
		AllowSends:   boolFromEnv(EnvAllowSends),
	}
}

// boolFromEnv reads an environment variable as a strict boolean: true only for
// an explicit "true" (case-insensitive, trimmed); anything else — unset, blank,
// or unrecognized — is false. It never requires the variable.
func boolFromEnv(name string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(name)), "true")
}

// Presence reports which configuration variables are set, as booleans only. It
// deliberately carries no values, so it is safe to log and to return from
// tools.
type Presence struct {
	ClientID     bool `json:"clientId" jsonschema:"whether GWS_CLIENT_ID is set"`
	ClientSecret bool `json:"clientSecret" jsonschema:"whether GWS_CLIENT_SECRET is set"`
}

// Presence reports which GWS_* variables are set without exposing any value.
func (c Config) Presence() Presence {
	return Presence{
		ClientID:     c.ClientID != "",
		ClientSecret: strings.TrimSpace(c.ClientSecret) != "",
	}
}

// Mode describes the server's operating mode. M0 always runs classic-delegated;
// the resource-server and powerful-application tiers land in M5/M8.
func (c Config) Mode() string {
	return ModeClassicDelegated
}

// RequirePersonal validates that the credentials classic-delegated mode needs
// are present: the OAuth client id and secret of a GCP "Desktop app" client. It
// returns a clear error naming every missing variable, or nil when both are
// set. It reports only variable names, never any value. ConfigFromEnv still
// requires nothing — this is the explicit gate that sign-in calls before
// starting the installed-app OAuth flow.
func (c Config) RequirePersonal() error {
	var missing []string
	if strings.TrimSpace(c.ClientID) == "" {
		missing = append(missing, EnvClientID)
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		missing = append(missing, EnvClientSecret)
	}
	if len(missing) > 0 {
		return fmt.Errorf("classic-delegated mode requires %s", strings.Join(missing, " and "))
	}
	return nil
}

// Redact maps a possibly-secret value to a log-safe marker: "set" when it holds
// a non-blank value, "unset" otherwise. It never returns the value itself.
func Redact(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unset"
	}
	return "set"
}
