package googleauth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

// countingSource is a fake oauth2.TokenSource that returns a fixed token and
// counts how many times it was built and called.
type countingSource struct {
	token string
	calls *int32
}

func (c countingSource) Token() (*oauth2.Token, error) {
	atomic.AddInt32(c.calls, 1)
	return &oauth2.Token{AccessToken: c.token}, nil
}

func TestDWDImpersonatesPerUserAndCaches(t *testing.T) {
	var built int32
	var calls int32
	d := &DWD{
		newSource: func(_ context.Context, subject string) (oauth2.TokenSource, error) {
			atomic.AddInt32(&built, 1)
			return countingSource{token: "token-for-" + subject, calls: &calls}, nil
		},
		sources: make(map[string]oauth2.TokenSource),
	}

	// First user.
	tok, err := d.GoogleToken(WithUser(context.Background(), "ada@example.com"))
	if err != nil {
		t.Fatalf("GoogleToken: %v", err)
	}
	if tok != "token-for-ada@example.com" {
		t.Errorf("token = %q", tok)
	}

	// Same user again → source is reused, not rebuilt.
	if _, err := d.GoogleToken(WithUser(context.Background(), "ada@example.com")); err != nil {
		t.Fatalf("GoogleToken: %v", err)
	}
	if built != 1 {
		t.Errorf("source built %d times for one user, want 1", built)
	}

	// Different user → a distinct source, impersonating that user.
	tok2, err := d.GoogleToken(WithUser(context.Background(), "grace@example.com"))
	if err != nil {
		t.Fatalf("GoogleToken: %v", err)
	}
	if tok2 != "token-for-grace@example.com" {
		t.Errorf("second user token = %q", tok2)
	}
	if built != 2 {
		t.Errorf("source built %d times for two users, want 2", built)
	}
}

func TestDWDRequiresUserOnContext(t *testing.T) {
	d := &DWD{
		newSource: func(context.Context, string) (oauth2.TokenSource, error) {
			t.Error("newSource must not be called without a user on context")
			return nil, nil
		},
		sources: make(map[string]oauth2.TokenSource),
	}
	if _, err := d.GoogleToken(context.Background()); !errors.Is(err, errNoUser) {
		t.Errorf("error = %v, want errNoUser", err)
	}
}

func TestUserContextRoundTrip(t *testing.T) {
	ctx := WithUser(context.Background(), "ada@example.com")
	got, ok := UserFromContext(ctx)
	if !ok || got != "ada@example.com" {
		t.Errorf("UserFromContext = %q, %v", got, ok)
	}
	if _, ok := UserFromContext(context.Background()); ok {
		t.Error("UserFromContext should report absent on a bare context")
	}
}

func TestNewDWDRejectsBadKey(t *testing.T) {
	// A nonexistent path must fail at construction, not per request.
	if _, err := NewDWD("/nonexistent/sa-key.json", []string{"scope"}); err == nil {
		t.Fatal("expected error reading a missing key file")
	}
}
