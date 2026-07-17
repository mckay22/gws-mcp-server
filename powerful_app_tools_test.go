package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/mckay22/gws-mcp-server/internal/googleauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// appTokenSource records the impersonation target from each request's context,
// so tests can assert the app tools impersonate the right user.
type appTokenSource struct {
	mu      sync.Mutex
	lastSub string
}

func (a *appTokenSource) GoogleToken(ctx context.Context) (string, error) {
	user, _ := googleauth.UserFromContext(ctx)
	a.mu.Lock()
	a.lastSub = user
	a.mu.Unlock()
	return "app-token", nil
}

type appCapture struct {
	mu      sync.Mutex
	paths   []string
	methods []string
	bodies  []string
}

func mockApp(t *testing.T) (*httptest.Server, *appCapture) {
	t.Helper()
	cap := &appCapture{}
	record := func(r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.paths = append(cap.paths, r.URL.Path)
		cap.methods = append(cap.methods, r.Method)
		cap.bodies = append(cap.bodies, string(b))
		cap.mu.Unlock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/{u}/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"messages":[{"id":"m1","threadId":"t1"}]}`)
	})
	mux.HandleFunc("POST /gmail/v1/users/{u}/messages/send", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"id":"sent1"}`)
	})
	mux.HandleFunc("PATCH /admin/directory/v1/users/{u}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		// One target fails to exercise per-item outcomes.
		if strings.Contains(r.URL.Path, "bad@example.com") {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"Not found","status":"NOT_FOUND"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"suspended":true}`)
	})
	mux.HandleFunc("POST /admin/directory/v1/groups/{g}/members", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"email":"added"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectApp(t *testing.T, srv *httptest.Server, cfg config.Config, ats *appTokenSource) (*mcp.ClientSession, *appTokenSource) {
	t.Helper()
	if ats == nil {
		ats = &appTokenSource{}
	}
	gc := gapi.New(ats, gapi.WithBaseURL(srv.URL))
	cs := connectRegistered(t, srv, func(s *mcp.Server, _ *gapi.Client) {
		registerAppTools(s, gc, cfg)
	})
	return cs, ats
}

func TestAppListMessagesImpersonatesTarget(t *testing.T) {
	srv, _ := mockApp(t)
	cs, ats := connectApp(t, srv, config.Config{}, nil)

	_, out := callTool(t, cs, "app_list_messages", map[string]any{"user": "target@example.com"})
	if out["count"] != float64(1) {
		t.Errorf("count = %v", out["count"])
	}
	ats.mu.Lock()
	defer ats.mu.Unlock()
	if ats.lastSub != "target@example.com" {
		t.Errorf("impersonated %q, want target@example.com", ats.lastSub)
	}
}

func TestAppListMessagesRequiresUser(t *testing.T) {
	srv, _ := mockApp(t)
	cs, _ := connectApp(t, srv, config.Config{}, nil)

	// A present-but-blank user exercises the handler's own check (the SDK rejects
	// an entirely-missing required field via schema validation).
	msg := callToolErr(t, cs, "app_list_messages", map[string]any{"user": "  "})
	if !strings.Contains(msg, "user is required") {
		t.Errorf("error = %q", msg)
	}
}

func TestAppSendMailRidesSendGate(t *testing.T) {
	srv, cap := mockApp(t)

	// Write gate open, send gate closed → dry-run.
	cs, _ := connectApp(t, srv, config.Config{AllowWrites: true}, nil)
	_, out := callTool(t, cs, "app_send_mail", map[string]any{
		"user": "sender@example.com", "to": []any{"rcpt@example.com"}, "subject": "Hi", "body": "hello",
	})
	if out["dryRun"] != true {
		t.Errorf("app_send_mail must dry-run when only the write gate is open: %v", out)
	}

	// Send gate open → applied, impersonating the sender.
	cs2, ats2 := connectApp(t, srv, config.Config{AllowSends: true}, nil)
	_, out2 := callTool(t, cs2, "app_send_mail", map[string]any{
		"user": "sender@example.com", "to": []any{"rcpt@example.com"}, "subject": "Hi", "body": "hello",
	})
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}
	ats2.mu.Lock()
	if ats2.lastSub != "sender@example.com" {
		t.Errorf("send impersonated %q, want sender@example.com", ats2.lastSub)
	}
	ats2.mu.Unlock()
	cap.mu.Lock()
	defer cap.mu.Unlock()
	joined := strings.Join(cap.paths, ",")
	if !strings.Contains(joined, "/gmail/v1/users/sender@example.com/messages/send") {
		t.Errorf("send path not recorded: %v", cap.paths)
	}
}

func TestAppBulkUserSuspendPerItemOutcomes(t *testing.T) {
	srv, _ := mockApp(t)
	cfg := config.Config{AllowWrites: true, AppAdminSubject: "admin@example.com"}
	cs, ats := connectApp(t, srv, cfg, nil)

	_, out := callTool(t, cs, "app_bulk_user_suspend", map[string]any{
		"users":   []any{"ok1@example.com", "bad@example.com", "ok2@example.com"},
		"suspend": true,
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	if out["appliedCount"] != float64(2) || out["errorCount"] != float64(1) {
		t.Errorf("counts = applied:%v errors:%v, want 2/1", out["appliedCount"], out["errorCount"])
	}
	results := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	// The bulk op impersonates the admin subject, not any target.
	ats.mu.Lock()
	defer ats.mu.Unlock()
	if ats.lastSub != "admin@example.com" {
		t.Errorf("bulk impersonated %q, want admin@example.com", ats.lastSub)
	}
}

func TestAppBulkRejectsDuplicates(t *testing.T) {
	srv, _ := mockApp(t)
	cfg := config.Config{AllowWrites: true, AppAdminSubject: "admin@example.com"}
	cs, _ := connectApp(t, srv, cfg, nil)

	msg := callToolErr(t, cs, "app_bulk_user_suspend", map[string]any{
		"users":   []any{"a@example.com", "a@example.com"},
		"suspend": true,
	})
	if !strings.Contains(msg, "duplicate target") {
		t.Errorf("error = %q, want duplicate rejection", msg)
	}
}

func TestAppBulkDryRunWhenGateClosed(t *testing.T) {
	srv, cap := mockApp(t)
	cfg := config.Config{AppAdminSubject: "admin@example.com"} // write gate closed
	cs, _ := connectApp(t, srv, cfg, nil)

	_, out := callTool(t, cs, "app_bulk_user_suspend", map[string]any{
		"users":   []any{"a@example.com", "b@example.com"},
		"suspend": true,
	})
	if out["dryRun"] != true {
		t.Errorf("expected dry-run, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.paths) != 0 {
		t.Errorf("dry-run made calls: %v", cap.paths)
	}
}

func TestAppBulkRequiresAdminSubject(t *testing.T) {
	srv, _ := mockApp(t)
	cfg := config.Config{AllowWrites: true} // no admin subject
	cs, _ := connectApp(t, srv, cfg, nil)

	msg := callToolErr(t, cs, "app_bulk_group_add_members", map[string]any{
		"groupKey": "eng@example.com",
		"members":  []any{"a@example.com"},
	})
	if !strings.Contains(msg, "GWS_APP_ADMIN_SUBJECT") {
		t.Errorf("error = %q, want admin-subject requirement", msg)
	}
}

func TestRequireAppOnlySeparation(t *testing.T) {
	// Same key for both tiers must be rejected.
	shared := config.Config{DWDKeyPath: "/keys/shared.json", AppKeyPath: "/keys/shared.json"}
	if err := shared.RequireAppOnly(); err == nil || !strings.Contains(err.Error(), "SEPARATE") {
		t.Errorf("RequireAppOnly(shared key) = %v, want a separation error", err)
	}
	// Missing app key.
	if err := (config.Config{}).RequireAppOnly(); err == nil {
		t.Error("RequireAppOnly with no app key should error")
	}
	// Distinct keys are fine.
	ok := config.Config{DWDKeyPath: "/keys/rs.json", AppKeyPath: "/keys/app.json"}
	if err := ok.RequireAppOnly(); err != nil {
		t.Errorf("RequireAppOnly(distinct keys) = %v, want nil", err)
	}
}
