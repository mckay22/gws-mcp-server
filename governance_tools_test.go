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
			{"id":{"time":"2026-07-16T10:00:00Z","applicationName":"login"},"actor":{"email":"ada@example.com"},"ipAddress":"203.0.113.5","events":[{"type":"login","name":"login_success"}]},
			{"id":{"time":"2026-07-16T11:00:00Z","applicationName":"drive"},"actor":{"email":"ada@example.com"},"events":[{"type":"access","name":"download","parameters":[
				{"name":"doc_title","value":"Q3 Board Deck"},
				{"name":"doc_id","value":"1a2b3c"},
				{"name":"owner_is_shared_drive","boolValue":false},
				{"name":"visibility_change","multiValue":["external","shared_externally"]},
				{"name":"billable","intValue":"42"},
				{"name":"empty_param"}
			]}]}
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

	_, out := callTool(t, cs, "admin_list_audit_activities", map[string]any{
		"application": "login",
		"startTime":   "2026-07-01T00:00:00Z",
	})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
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

// TestAuditActivitiesReturnsEventParameters is the regression test for an audit
// tool that could report what KIND of thing happened but never what it happened
// TO: the fields projection dropped events.parameters, so a Drive row said
// "download" with no document and an admin row had no target. Google returns the
// value in one of four typed fields, all of which must be flattened.
func TestAuditActivitiesReturnsEventParameters(t *testing.T) {
	srv, cap := mockGovernance(t)
	cs := connectGovernance(t, srv)

	_, out := callTool(t, cs, "admin_list_audit_activities", map[string]any{"application": "drive"})

	cap.mu.Lock()
	q := cap.activityQ
	cap.mu.Unlock()
	if !strings.Contains(q, "parameters") {
		t.Errorf("fields projection does not request event parameters: %q", q)
	}

	acts := out["activities"].([]any)
	if len(acts) < 2 {
		t.Fatalf("want 2 activities, got %d", len(acts))
	}
	events := acts[1].(map[string]any)["events"].([]any)
	params, ok := events[0].(map[string]any)["parameters"].([]any)
	if !ok || len(params) == 0 {
		t.Fatalf("download event carries no parameters: %v", events[0])
	}
	got := map[string]string{}
	for _, p := range params {
		m := p.(map[string]any)
		name, _ := m["name"].(string)
		value, _ := m["value"].(string)
		got[name] = value
	}
	for name, want := range map[string]string{
		"doc_title":             "Q3 Board Deck",               // string value
		"doc_id":                "1a2b3c",                      // string value
		"owner_is_shared_drive": "false",                       // boolValue
		"visibility_change":     "external, shared_externally", // multiValue
		"billable":              "42",                          // intValue (string-encoded int64)
	} {
		if got[name] != want {
			t.Errorf("parameter %q = %q, want %q", name, got[name], want)
		}
	}
	if _, present := got["empty_param"]; present {
		t.Error("a parameter with no value should be omitted, not reported as empty")
	}
}

func TestAuditActivitiesRequiresApplication(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	// A blank application no longer reaches the handler: the schema enum rejects
	// it and lists the applications that are actually auditable.
	msg := callToolErr(t, cs, "admin_list_audit_activities", map[string]any{"application": "  "})
	for _, want := range []string{"application", "login", "drive"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to mention %q", msg, want)
		}
	}
}

func TestAuditActivitiesValidatesTime(t *testing.T) {
	srv, _ := mockGovernance(t)
	cs := connectGovernance(t, srv)

	msg := callToolErr(t, cs, "admin_list_audit_activities", map[string]any{
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

	_, out := callTool(t, cs, "admin_list_connected_apps", map[string]any{"userKey": "ada@example.com"})
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

	_, out := callTool(t, cs, "admin_list_license_assignments", map[string]any{
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

	msg := callToolErr(t, cs, "admin_list_license_assignments", map[string]any{"productId": "Google-Apps"})
	if !strings.Contains(msg, "customerId") {
		t.Errorf("error = %q", msg)
	}
}
