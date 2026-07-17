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

	// EnvAdmin registers the Admin SDK Directory tools (users, groups, members,
	// admin roles) and requests the sensitive admin.directory.*.readonly scopes.
	// It is a REGISTRATION switch, not a gate: it only makes sense when the
	// signed-in user holds a Workspace/Cloud Identity admin role, so it is opt-in
	// — consumer @gmail.com accounts leave it off and keep a lean tool list.
	// Unset or anything other than "true" (case-insensitive) leaves the directory
	// tools unregistered and their scopes unrequested.
	EnvAdmin = "GWS_MCP_ADMIN"

	// EnvAudience is the audience that incoming bearer tokens must be minted for
	// in resource-server mode (this server's identifier). Serving over --http
	// requires it. It carries no secret.
	EnvAudience = "GWS_AUDIENCE"

	// EnvIssuers is a comma-separated allowlist of trusted OIDC issuer URLs whose
	// tokens the resource-server verifier accepts (e.g. a Keycloak realm, an Entra
	// tenant v2.0 issuer, or Google's accounts issuer). The verifier is
	// issuer-agnostic: each issuer's OIDC metadata (and JWKS) is discovered.
	EnvIssuers = "GWS_ISSUERS"

	// EnvDWDKeyPath is the filesystem path to the domain-wide-delegation service
	// account's JSON key. The key is a domain credential (it can mint tokens as
	// any user in the domain within the DWD-granted scopes), so it is provided by
	// path at runtime, loaded outside the repo, and never logged. Its presence is
	// reported as a boolean only.
	EnvDWDKeyPath = "GWS_DWD_SA_KEY"

	// EnvSubjectClaim is the verified-token claim mapped to the Google user the
	// DWD backend impersonates (the minted JWT's sub). Default "email".
	EnvSubjectClaim = "GWS_SUBJECT_CLAIM"
)

// DefaultSubjectClaim is the token claim used to map a verified caller to a
// Google identity when EnvSubjectClaim is unset.
const DefaultSubjectClaim = "email"

// ModeResourceServer is the multi-user operating mode: the server validates each
// request's bearer token against a trusted OIDC issuer and acts as the mapped
// caller via the DWD identity backend.
const ModeResourceServer = "resource-server"

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

	// Admin registers the Admin SDK Directory tools and requests the admin
	// directory readonly scopes. Registration only — a directory read still
	// requires the signed-in user to hold the matching admin privilege, which
	// Google enforces. Set by GWS_MCP_ADMIN=true or --admin; carries no secret.
	Admin bool

	// Audience is the resource-server audience incoming bearer tokens must carry.
	// Empty means classic-delegated mode; a non-empty value plus --http selects
	// resource-server mode. It carries no secret.
	Audience string

	// AllowedIssuers is the parsed GWS_ISSUERS allowlist of trusted OIDC issuer
	// URLs whose tokens the verifier accepts. It carries no secret.
	AllowedIssuers []string

	// DWDKeyPath is the path to the domain-wide-delegation service account JSON
	// key. The file contents are a domain credential and are never logged; expose
	// presence via Presence instead.
	DWDKeyPath string

	// SubjectClaim is the verified-token claim mapped to the impersonated Google
	// user (default DefaultSubjectClaim). It carries no secret.
	SubjectClaim string
}

// ConfigFromEnv builds a Config from the GWS_* environment variables. It does
// not require any of them: because M0 is a scaffold and auth lands in M1, a
// missing variable is reported (as a presence boolean), not treated as an
// error.
func ConfigFromEnv() Config {
	return Config{
		ClientID:       strings.TrimSpace(os.Getenv(EnvClientID)),
		ClientSecret:   os.Getenv(EnvClientSecret),
		AllowWrites:    boolFromEnv(EnvAllowWrites),
		AllowSends:     boolFromEnv(EnvAllowSends),
		Admin:          boolFromEnv(EnvAdmin),
		Audience:       strings.TrimSpace(os.Getenv(EnvAudience)),
		AllowedIssuers: listFromEnv(EnvIssuers),
		DWDKeyPath:     strings.TrimSpace(os.Getenv(EnvDWDKeyPath)),
		SubjectClaim:   strings.TrimSpace(os.Getenv(EnvSubjectClaim)),
	}
}

// listFromEnv parses a comma-separated env var into a trimmed, blank-free slice.
// It returns nil when the variable is unset or holds only blanks.
func listFromEnv(name string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(name), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
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
	DWDKey       bool `json:"dwdKey,omitempty" jsonschema:"whether GWS_DWD_SA_KEY (resource-server DWD key) is set"`
}

// Presence reports which GWS_* variables are set without exposing any value.
func (c Config) Presence() Presence {
	return Presence{
		ClientID:     c.ClientID != "",
		ClientSecret: strings.TrimSpace(c.ClientSecret) != "",
		DWDKey:       c.DWDKeyPath != "",
	}
}

// Mode describes the server's operating mode: resource-server when an audience
// is configured (multi-user, bearer-validated), otherwise classic-delegated.
func (c Config) Mode() string {
	if c.ResourceServerMode() {
		return ModeResourceServer
	}
	return ModeClassicDelegated
}

// ResourceServerMode reports whether the server should validate incoming bearer
// tokens and act as the mapped caller (rather than a single signed-in user). It
// is selected by GWS_AUDIENCE being set.
func (c Config) ResourceServerMode() bool {
	return strings.TrimSpace(c.Audience) != ""
}

// RequireResourceServer validates everything resource-server mode needs: an
// audience for the incoming-token verifier, at least one trusted issuer, and the
// domain-wide-delegation service-account key path the DWD backend signs with. It
// returns a clear error naming every missing variable, never any value.
func (c Config) RequireResourceServer() error {
	var missing []string
	if strings.TrimSpace(c.Audience) == "" {
		missing = append(missing, EnvAudience)
	}
	if len(c.Issuers()) == 0 {
		missing = append(missing, EnvIssuers)
	}
	if strings.TrimSpace(c.DWDKeyPath) == "" {
		missing = append(missing, EnvDWDKeyPath)
	}
	if len(missing) > 0 {
		return fmt.Errorf("resource-server mode requires %s", strings.Join(missing, ", "))
	}
	return nil
}

// Issuers returns the trusted OIDC issuer allowlist for the verifier.
func (c Config) Issuers() []string {
	return c.AllowedIssuers
}

// SubjectClaimOrDefault returns the configured token claim to map to a Google
// user, or DefaultSubjectClaim ("email") when unset.
func (c Config) SubjectClaimOrDefault() string {
	if s := strings.TrimSpace(c.SubjectClaim); s != "" {
		return s
	}
	return DefaultSubjectClaim
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
