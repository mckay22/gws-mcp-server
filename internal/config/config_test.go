package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvClientID, "  1234567890-abc.apps.googleusercontent.com  ")
	t.Setenv(EnvClientSecret, "test-secret-value")
	t.Setenv(EnvAllowWrites, "true")
	t.Setenv(EnvAllowSends, "")

	cfg := ConfigFromEnv()
	if cfg.ClientID != "1234567890-abc.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q, want trimmed value", cfg.ClientID)
	}
	if cfg.ClientSecret != "test-secret-value" {
		t.Errorf("ClientSecret not preserved verbatim")
	}
	if !cfg.AllowWrites {
		t.Error("AllowWrites = false, want true")
	}
	if cfg.AllowSends {
		t.Error("AllowSends = true, want false")
	}
}

func TestGatesParseStrictly(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"TRUE", true},
		{"  True  ", true},
		{"", false},
		{"false", false},
		{"1", false},
		{"yes", false},
		{"on", false},
	}
	for _, tc := range cases {
		t.Setenv(EnvAllowWrites, tc.value)
		t.Setenv(EnvAllowSends, tc.value)
		cfg := ConfigFromEnv()
		if cfg.AllowWrites != tc.want {
			t.Errorf("AllowWrites(%q) = %t, want %t", tc.value, cfg.AllowWrites, tc.want)
		}
		if cfg.AllowSends != tc.want {
			t.Errorf("AllowSends(%q) = %t, want %t", tc.value, cfg.AllowSends, tc.want)
		}
	}
}

func TestPresence(t *testing.T) {
	empty := Config{}.Presence()
	if empty.ClientID || empty.ClientSecret {
		t.Errorf("empty config presence = %+v, want all false", empty)
	}

	set := Config{ClientID: "id", ClientSecret: "secret"}.Presence()
	if !set.ClientID || !set.ClientSecret {
		t.Errorf("set config presence = %+v, want all true", set)
	}

	// A whitespace-only secret counts as unset.
	blank := Config{ClientSecret: "   "}.Presence()
	if blank.ClientSecret {
		t.Error("whitespace-only ClientSecret reported as present")
	}
}

func TestMode(t *testing.T) {
	if got := (Config{}).Mode(); got != ModeClassicDelegated {
		t.Errorf("Mode() = %q, want %q", got, ModeClassicDelegated)
	}
	rs := Config{Audience: "api://gws"}
	if got := rs.Mode(); got != ModeResourceServer {
		t.Errorf("Mode() = %q, want %q when audience set", got, ModeResourceServer)
	}
	if !rs.ResourceServerMode() {
		t.Error("ResourceServerMode() = false, want true when audience set")
	}
}

func TestRequireResourceServer(t *testing.T) {
	// Missing everything → error naming all three.
	err := Config{}.RequireResourceServer()
	if err == nil {
		t.Fatal("expected error with no resource-server config")
	}
	for _, want := range []string{EnvAudience, EnvIssuers, EnvDWDKeyPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %s", err, want)
		}
	}
	// Fully configured → nil.
	ok := Config{
		Audience:       "api://gws",
		AllowedIssuers: []string{"https://issuer.example.com"},
		DWDKeyPath:     "/keys/sa.json",
	}
	if err := ok.RequireResourceServer(); err != nil {
		t.Errorf("unexpected error with full config: %v", err)
	}
}

func TestSubjectClaimOrDefault(t *testing.T) {
	if got := (Config{}).SubjectClaimOrDefault(); got != DefaultSubjectClaim {
		t.Errorf("SubjectClaimOrDefault() = %q, want %q", got, DefaultSubjectClaim)
	}
	if got := (Config{SubjectClaim: "preferred_username"}).SubjectClaimOrDefault(); got != "preferred_username" {
		t.Errorf("SubjectClaimOrDefault() = %q, want the configured claim", got)
	}
}

func TestDWDKeyPresence(t *testing.T) {
	if (Config{}).Presence().DWDKey {
		t.Error("DWDKey presence should be false when unset")
	}
	if !(Config{DWDKeyPath: "/keys/sa.json"}).Presence().DWDKey {
		t.Error("DWDKey presence should be true when path set")
	}
}

func TestRedact(t *testing.T) {
	if got := Redact(""); got != "unset" {
		t.Errorf("Redact(empty) = %q, want unset", got)
	}
	if got := Redact("   "); got != "unset" {
		t.Errorf("Redact(blank) = %q, want unset", got)
	}
	got := Redact("super-secret")
	if got != "set" {
		t.Errorf("Redact(value) = %q, want set", got)
	}
	if strings.Contains(got, "super-secret") {
		t.Error("Redact leaked the value")
	}
}

// TestRequireAppOnlyCatchesAliasedKey covers the tier-separation invariant: the
// application tier must not be handed the resource-server's credential. A plain
// string compare misses a symlink or an alternate spelling of the same file.
func TestRequireAppOnlyCatchesAliasedKey(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dwd-sa.json")
	if err := os.WriteFile(real, []byte(`{"client_email":"a@b.iam.gserviceaccount.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "app-sa.json")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		name            string
		app, dwd        string
		wantSeparateErr bool
	}{
		{name: "same path", app: real, dwd: real, wantSeparateErr: true},
		{name: "symlink to the same file", app: link, dwd: real, wantSeparateErr: true},
		{name: "distinct files", app: filepath.Join(dir, "other.json"), dwd: real},
	}
	for _, tc := range cases {
		cfg := Config{AppKeyPath: tc.app, DWDKeyPath: tc.dwd}
		err := cfg.RequireAppOnly()
		if tc.wantSeparateErr {
			if err == nil || !strings.Contains(err.Error(), "SEPARATE") {
				t.Errorf("%s: err = %v, want a separate-key rejection", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected err %v", tc.name, err)
		}
	}
}
