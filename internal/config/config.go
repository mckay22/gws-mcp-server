// Package config parses the gws-mcp-server runtime configuration from the
// environment. Credentials are read at runtime and never written to disk, logs,
// or tool output — only presence booleans are ever exposed.
//
// Parsing never fails on a missing variable: what a given mode actually requires
// is stated by the Require* methods, which the relevant entry point calls, so a
// deployment that uses only one mode is not forced to configure the others.
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

	// EnvTrustUnverifiedEmail opts OUT of the email_verified safety check. When
	// the subject claim is "email", the verifier by default requires the token to
	// carry email_verified==true before impersonating that Google user — an
	// unverified/mutable email would otherwise let a caller be impersonated as an
	// arbitrary Workspace user. Set to "true" ONLY when every trusted issuer is
	// guaranteed to assert verified emails; it is a security-relevant relaxation.
	EnvTrustUnverifiedEmail = "GWS_TRUST_UNVERIFIED_EMAIL"

	// EnvPowerful registers the powerful-delegated end-user tools (Gmail
	// settings, Tasks, People, Chat, Meet, Drive shared-with-me). It is a
	// REGISTRATION switch, not a gate: the tools it exposes still honor the
	// write/send gates. Some (Chat, Meet) are Workspace-only and error cleanly on
	// consumer accounts. Unset or anything other than "true" leaves them
	// unregistered so lean deployments keep a small tool list.
	EnvPowerful = "GWS_MCP_POWERFUL"

	// EnvAppOnly enables the powerful-application tier: app_* tools that act on
	// ANY principal via domain-wide delegation, targeting an explicit user
	// parameter. Never the default; requires its OWN service account
	// (EnvAppKeyPath) that must differ from the resource-server DWD key, so a
	// leaked resource-server key cannot escalate. Like the other switches the
	// tools still honor the write/send gates.
	EnvAppOnly = "GWS_MCP_APP_ONLY"

	// EnvAppKeyPath is the application tier's OWN domain-wide-delegation
	// service-account JSON key path — never shared with the resource-server
	// backend's key. Its contents are a domain credential and are never logged.
	EnvAppKeyPath = "GWS_APP_SA_KEY"

	// EnvAppAdminSubject is the admin user the application-tier SA impersonates
	// for Directory admin operations (bulk user/group lifecycle). Per-user
	// mailbox/calendar/drive tools impersonate their own target instead, so this
	// is required only for the bulk directory tools.
	EnvAppAdminSubject = "GWS_APP_ADMIN_SUBJECT"
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
// on every call.
const ModeClassicDelegated = "classic-delegated"

// Config holds everything needed to reach the Google APIs.
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

	// TrustUnverifiedEmail opts out of requiring email_verified==true when the
	// subject claim is "email" (see EnvTrustUnverifiedEmail). Default false: the
	// verifier enforces the check. Carries no secret.
	TrustUnverifiedEmail bool

	// Powerful registers the powerful-delegated end-user tools (Gmail settings,
	// Tasks, People, Chat, Meet, Drive shared-with-me). Registration only — those
	// tools still honor AllowWrites/AllowSends. Set by GWS_MCP_POWERFUL=true or
	// --powerful; carries no secret.
	Powerful bool

	// AppOnly enables the powerful-application tier (app_* tools acting on an
	// explicit user target). Registration only — the tools still honor the gates.
	// Set by GWS_MCP_APP_ONLY=true or --app-only; requires AppKeyPath (see
	// RequireAppOnly).
	AppOnly bool

	// AppKeyPath is the application tier's own DWD service-account key path. Its
	// contents are never logged; expose presence via Presence.
	AppKeyPath string

	// AppAdminSubject is the admin user the application-tier SA impersonates for
	// bulk Directory operations. It carries no secret.
	AppAdminSubject string
}

// ConfigFromEnv builds a Config from the GWS_* environment variables. It requires
// none of them: a missing variable is reported (as a presence boolean), and
// whether it is actually needed is decided by the Require* method for the mode
// being started.
func ConfigFromEnv() Config {
	return Config{
		ClientID:             strings.TrimSpace(os.Getenv(EnvClientID)),
		ClientSecret:         os.Getenv(EnvClientSecret),
		AllowWrites:          boolFromEnv(EnvAllowWrites),
		AllowSends:           boolFromEnv(EnvAllowSends),
		Admin:                boolFromEnv(EnvAdmin),
		Audience:             strings.TrimSpace(os.Getenv(EnvAudience)),
		AllowedIssuers:       listFromEnv(EnvIssuers),
		DWDKeyPath:           strings.TrimSpace(os.Getenv(EnvDWDKeyPath)),
		SubjectClaim:         strings.TrimSpace(os.Getenv(EnvSubjectClaim)),
		TrustUnverifiedEmail: boolFromEnv(EnvTrustUnverifiedEmail),
		Powerful:             boolFromEnv(EnvPowerful),
		AppOnly:              boolFromEnv(EnvAppOnly),
		AppKeyPath:           strings.TrimSpace(os.Getenv(EnvAppKeyPath)),
		AppAdminSubject:      strings.TrimSpace(os.Getenv(EnvAppAdminSubject)),
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
	AppKey       bool `json:"appKey,omitempty" jsonschema:"whether GWS_APP_SA_KEY (application-tier key) is set"`
}

// Presence reports which GWS_* variables are set without exposing any value.
func (c Config) Presence() Presence {
	return Presence{
		ClientID:     c.ClientID != "",
		ClientSecret: strings.TrimSpace(c.ClientSecret) != "",
		DWDKey:       c.DWDKeyPath != "",
		AppKey:       c.AppKeyPath != "",
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

// RequireAppOnly validates the powerful-application tier: its own service-account
// key must be configured, and it must be a SEPARATE key from the resource-server
// DWD key (GWS_DWD_SA_KEY) — the tiers never share a credential, so a leaked
// resource-server key cannot escalate to the application tier. It reports only
// variable names, never any value.
func (c Config) RequireAppOnly() error {
	if strings.TrimSpace(c.AppKeyPath) == "" {
		return fmt.Errorf("app-only mode requires %s", EnvAppKeyPath)
	}
	app := strings.TrimSpace(c.AppKeyPath)
	dwd := strings.TrimSpace(c.DWDKeyPath)
	if dwd == "" {
		return nil
	}
	if dwd == app || sameFile(dwd, app) {
		// sameFile catches the aliases a string compare misses: a symlink, a
		// relative vs absolute spelling, a hard link, or a bind mount. A *copy* of
		// the same key has a different inode and is caught later, by the key
		// identity check where the two credentials are actually loaded.
		return fmt.Errorf("%s must be a SEPARATE key from %s — the application tier never shares the resource-server credential", EnvAppKeyPath, EnvDWDKeyPath)
	}
	return nil
}

// sameFile reports whether two paths resolve to the same file on disk, so a
// symlink or an alternate spelling cannot pass off one credential as two. A path
// that cannot be stat'ed is not treated as a match — the subsequent load reports
// the real error.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
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
