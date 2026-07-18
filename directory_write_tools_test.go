package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type dirWriteCapture struct {
	mu     sync.Mutex
	called bool
	method string
	path   string
	body   string
}

func mockDirectoryWrite(t *testing.T) (*httptest.Server, *dirWriteCapture) {
	t.Helper()
	cap := &dirWriteCapture{}
	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"new-id","primaryEmail":"new@example.com"}`)
	}
	mux.HandleFunc("POST /admin/directory/v1/users", handler)
	mux.HandleFunc("PATCH /admin/directory/v1/users/{key}", handler)
	mux.HandleFunc("POST /admin/directory/v1/groups", handler)
	mux.HandleFunc("POST /admin/directory/v1/groups/{key}/members", handler)
	mux.HandleFunc("DELETE /admin/directory/v1/groups/{key}/members/{m}", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectDirectoryWrite(t *testing.T, srv *httptest.Server, allowWrites bool) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, func(s *mcp.Server, gc *gapi.Client) {
		registerDirectoryWriteTools(s, gc, allowWrites, false)
	})
}

func TestDirectoryUserCreateRedactsPasswordInPreview(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, false) // write gate closed → dry-run

	res, out := callTool(t, cs, "directory_create_user", map[string]any{
		"primaryEmail": "new@example.com",
		"givenName":    "New",
		"familyName":   "User",
		"password":     "super-secret-pw",
	})
	if out["dryRun"] != true {
		t.Errorf("expected dry-run, got %v", out)
	}
	if cap.called {
		t.Error("dry-run must not call the API")
	}
	// The password must not appear anywhere in the preview result.
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "super-secret-pw") {
		t.Error("dry-run preview leaked the password")
	}
	body := out["body"].(map[string]any)
	if body["password"] != "REDACTED" {
		t.Errorf("preview password = %v, want REDACTED", body["password"])
	}
}

func TestDirectoryUserCreateAppliesRealPassword(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, true) // write gate open

	res, out := callTool(t, cs, "directory_create_user", map[string]any{
		"primaryEmail": "new@example.com",
		"givenName":    "New",
		"familyName":   "User",
		"password":     "super-secret-pw",
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	// The applied output still shows the redacted form (never the real password).
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "super-secret-pw") {
		t.Error("applied output leaked the password")
	}
	// But the wire carried the real password.
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.body, "super-secret-pw") {
		t.Errorf("wire body should carry the real password, got %q", cap.body)
	}
}

func TestDirectoryUserSuspend(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, true)

	_, out := callTool(t, cs, "directory_suspend_user", map[string]any{
		"userKey": "ada@example.com",
		"suspend": true,
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodPatch || !strings.Contains(cap.body, `"suspended":true`) {
		t.Errorf("recorded %s body=%q", cap.method, cap.body)
	}
}

func TestDirectoryGroupAddMemberValidatesRole(t *testing.T) {
	srv, _ := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, true)

	msg := callToolErr(t, cs, "directory_add_group_member", map[string]any{
		"groupKey": "eng@example.com",
		"email":    "ada@example.com",
		"role":     "SUPERUSER",
	})
	if !strings.Contains(msg, "role must be") {
		t.Errorf("error = %q", msg)
	}
}

func TestDirectoryGroupRemoveMember(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, true)

	_, out := callTool(t, cs, "directory_remove_group_member", map[string]any{
		"groupKey":  "eng@example.com",
		"memberKey": "ada@example.com",
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", cap.method)
	}
	if cap.path != "/admin/directory/v1/groups/eng@example.com/members/ada@example.com" {
		t.Errorf("path = %q", cap.path)
	}
}

func TestDirectoryWritesRideWriteGate(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)
	cs := connectDirectoryWrite(t, srv, false) // gate closed

	_, out := callTool(t, cs, "directory_create_group", map[string]any{"email": "new@example.com"})
	if out["dryRun"] != true {
		t.Errorf("directory write must dry-run with the write gate closed: %v", out)
	}
	if cap.called {
		t.Error("must not call the API when the write gate is closed")
	}
}

// directory_update_user had no behavioral test: it must PATCH the named user and
// send only the fields the caller supplied, since a Directory PATCH overwrites
// what it receives.
func TestDirectoryUpdateUserPatchesNamedFields(t *testing.T) {
	srv, cap := mockDirectoryWrite(t)

	// Gate closed → dry run, no call.
	cs := connectDirectoryWrite(t, srv, false)
	_, out := callTool(t, cs, "directory_update_user", map[string]any{
		"userKey":   "ada@example.com",
		"givenName": "Augusta",
	})
	if out["dryRun"] != true {
		t.Errorf("expected a dry run: %v", out)
	}
	if cap.called {
		t.Error("dry run wrote to the directory")
	}

	// Gate open → applied.
	cs2 := connectDirectoryWrite(t, srv, true)
	_, out2 := callTool(t, cs2, "directory_update_user", map[string]any{
		"userKey":     "ada@example.com",
		"givenName":   "Augusta",
		"orgUnitPath": "/Engineering",
	})
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", cap.method)
	}
	if cap.path != "/admin/directory/v1/users/ada@example.com" {
		t.Errorf("path = %q", cap.path)
	}
	if !strings.Contains(cap.body, "Augusta") || !strings.Contains(cap.body, "/Engineering") {
		t.Errorf("body = %q, want the named fields", cap.body)
	}
	// A field the caller did not name must not be sent; PATCH would overwrite it.
	if strings.Contains(cap.body, "familyName") {
		t.Errorf("body = %q, must not send unnamed fields", cap.body)
	}
}
