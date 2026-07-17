package config

import (
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
