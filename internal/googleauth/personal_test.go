package googleauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"golang.org/x/oauth2"
)

func TestNewPersonalRequiresCredentials(t *testing.T) {
	if _, err := NewPersonal(config.Config{}, []string{"scope"}); err == nil {
		t.Fatal("expected error with no client credentials")
	}
	if _, err := NewPersonal(config.Config{ClientID: "id"}, nil); err == nil {
		t.Fatal("expected error with client id but no secret")
	}
	if _, err := NewPersonal(config.Config{ClientID: "id", ClientSecret: "sec"}, nil); err != nil {
		t.Fatalf("unexpected error with full credentials: %v", err)
	}
}

// TestGoogleTokenSignsInOnceAndRefreshes verifies that the first GoogleToken
// call runs the interactive authorize exactly once, and that subsequent calls
// serve/refresh from the cached oauth2 source without re-authorizing. The
// initial token is seeded already-expired so the very first Token() call must
// refresh against the (fake) token endpoint — exercising the refresh path.
func TestGoogleTokenSignsInOnceAndRefreshes(t *testing.T) {
	var refreshHits int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshHits, 1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token request: %v", err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "refresh-tok" {
			t.Errorf("unexpected token request: %v", r.PostForm)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed-access","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenSrv.Close()

	var authCalls int32
	p := &Personal{
		oauth: &oauth2.Config{
			ClientID:     "id",
			ClientSecret: "sec",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenSrv.URL, AuthStyle: oauth2.AuthStyleInParams},
		},
		authorize: func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
			atomic.AddInt32(&authCalls, 1)
			return &oauth2.Token{
				AccessToken:  "initial-access",
				RefreshToken: "refresh-tok",
				Expiry:       time.Now().Add(-time.Hour), // already expired → forces a refresh
			}, nil
		},
	}

	got, err := p.GoogleToken(context.Background())
	if err != nil {
		t.Fatalf("GoogleToken: %v", err)
	}
	if got != "refreshed-access" {
		t.Errorf("token = %q, want refreshed-access", got)
	}

	// A second call must not re-authorize; the fresh (non-expired) token is
	// served from the cache without another refresh.
	if _, err := p.GoogleToken(context.Background()); err != nil {
		t.Fatalf("second GoogleToken: %v", err)
	}
	if authCalls != 1 {
		t.Errorf("authorize called %d times, want exactly 1", authCalls)
	}
	if refreshHits != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (second call served from cache)", refreshHits)
	}
}

func TestGoogleTokenPropagatesSignInError(t *testing.T) {
	p := &Personal{
		oauth: &oauth2.Config{},
		authorize: func(_ context.Context, _ *oauth2.Config) (*oauth2.Token, error) {
			return nil, errors.New("user cancelled")
		},
	}
	_, err := p.GoogleToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "google sign-in") {
		t.Fatalf("error = %v, want a wrapped sign-in error", err)
	}
}
