package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type govCapture struct {
	mu          sync.Mutex
	activityApp string
	activityQ   string
}

func mockGovernance(t *testing.T) (*httptest.Server, *govCapture) {
	t.Helper()
	cap := &govCapture{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/reports/v1/activity/users/{key}/applications/{app}", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.activityApp = r.PathValue("app")
		cap.activityQ = r.URL.RawQuery
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"items":[
			{"id":{"time":"2026-07-16T10:00:00Z","applicationName":"login"},"actor":{"email":"ada@example.com"},"ipAddress":"203.0.113.5","events":[{"type":"login","name":"login_success"}]}
		],"nextPageToken":"aNext"}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/users/{key}/tokens", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[
			{"clientId":"12345.apps.googleusercontent.com","displayText":"Third Party App","scopes":["https://www.googleapis.com/auth/drive.readonly"],"nativeApp":false}
		]}`)
	})

	mux.HandleFunc("GET /apps/licensing/v1/product/{prod}/users", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[
			{"userId":"ada@example.com","productId":"Google-Apps","skuId":"1010020020","skuName":"Google Workspace Business Standard"}
		]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectGovernance(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, registerGovernanceReadTools)
}

func TestAuditActivities(t *testing.T) {
	srv, cap := mockGovernance(t)
	cs := connectGovernance(t, srv)

	_, out := callTool(t, cs, "audit_activities", map[string]any{
		"application": "login",
		"startTime":   "2026-07-01T00:00:00Z",
	})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
	acts := out["activities"].([]any)
	first := acts[0].(map[string]any)
	if first["actorEmail"] != "ada@example.com" || first["ipAddress"] != "203.0.113.5" {
		t.Errorf("activity = %v", first)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.activityApp != "login" {
		t.Errorf("application = %q", cap.activityApp)
	}
	if !strings.Contains(cap.activityQ, "startTime=2026-07-01") {
		t.Errorf("query = %q, want startTime", cap.activityQ)
	}
}

func TestAuditActivitiesRequiresApplication(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	msg := callToolErr(t, cs, "audit_activities", map[string]any{"application": "  "})
	if !strings.Contains(msg, "application is required") {
		t.Errorf("error = %q", msg)
	}
}

func TestAuditActivitiesValidatesTime(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	msg := callToolErr(t, cs, "audit_activities", map[string]any{
		"application": "login",
		"startTime":   "yesterday",
	})
	if !strings.Contains(msg, "RFC3339") {
		t.Errorf("error = %q", msg)
	}
}

func TestConnectedApps(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	_, out := callTool(t, cs, "user_connected_apps", map[string]any{"userKey": "ada@example.com"})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
	apps := out["apps"].([]any)
	first := apps[0].(map[string]any)
	if first["clientId"] != "12345.apps.googleusercontent.com" {
		t.Errorf("app = %v", first)
	}
}

func TestLicenseAssignments(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	_, out := callTool(t, cs, "license_assignments", map[string]any{
		"productId":  "Google-Apps",
		"customerId": "C01abc",
	})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
}

func TestLicenseAssignmentsRequiresCustomer(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	msg := callToolErr(t, cs, "license_assignments", map[string]any{"productId": "Google-Apps"})
	if !strings.Contains(msg, "customerId") {
		t.Errorf("error = %q", msg)
	}
}
