package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type directoryCapture struct {
	mu       sync.Mutex
	usersQ   string
	customer string
	groupsUK string
}

func mockDirectory(t *testing.T) (*httptest.Server, *directoryCapture) {
	t.Helper()
	cap := &directoryCapture{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /admin/directory/v1/users", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.usersQ = r.URL.Query().Get("query")
		cap.customer = r.URL.Query().Get("customer")
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"users":[
			{"id":"1","primaryEmail":"ada@example.com","name":{"fullName":"Ada Lovelace"},"isAdmin":true,"orgUnitPath":"/"},
			{"id":"2","primaryEmail":"grace@example.com","name":{"fullName":"Grace Hopper"},"suspended":true,"orgUnitPath":"/Eng"}
		],"nextPageToken":"uNext"}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/users/{key}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("key") == "missing@example.com" {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"Resource Not Found: userKey","status":"NOT_FOUND"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"id":"1","primaryEmail":"ada@example.com","name":{"fullName":"Ada Lovelace","givenName":"Ada"},"isAdmin":true,"aliases":["ada.l@example.com"]}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.groupsUK = r.URL.Query().Get("userKey")
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"groups":[
			{"id":"g1","email":"eng@example.com","name":"Engineering","directMembersCount":"12","adminCreated":true}
		]}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/groups/{key}/members", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"members":[
			{"id":"1","email":"ada@example.com","role":"OWNER","type":"USER","status":"ACTIVE"},
			{"id":"2","email":"grace@example.com","role":"MEMBER","type":"USER","status":"ACTIVE"}
		]}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/customer/my_customer/roles", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[
			{"roleId":"1001","roleName":"_SEED_ADMIN_ROLE","isSystemRole":true,"isSuperAdminRole":true},
			{"roleId":"2002","roleName":"Help Desk","roleDescription":"Reset passwords","isSystemRole":true}
		]}`)
	})

	mux.HandleFunc("GET /admin/directory/v1/customer/my_customer/roleassignments", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[
			{"roleAssignmentId":"a1","roleId":"1001","assignedTo":"1","scopeType":"CUSTOMER"}
		]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectDirectory(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, registerDirectoryReadTools)
}

func TestDirectoryUsersSearch(t *testing.T) {
	srv, cap := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_search_users", map[string]any{"query": "isAdmin=true"})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	if out["nextPageToken"] != "uNext" {
		t.Errorf("nextPageToken = %v", out["nextPageToken"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.usersQ != "isAdmin=true" {
		t.Errorf("query = %q", cap.usersQ)
	}
	if cap.customer != "my_customer" {
		t.Errorf("customer = %q, want my_customer", cap.customer)
	}
}

func TestDirectoryUserGet(t *testing.T) {
	srv, _ := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_get_user", map[string]any{"userKey": "ada@example.com"})
	if out["primaryEmail"] != "ada@example.com" {
		t.Errorf("primaryEmail = %v", out["primaryEmail"])
	}
	aliases, ok := out["aliases"].([]any)
	if !ok || len(aliases) != 1 {
		t.Errorf("aliases = %v", out["aliases"])
	}
}

func TestDirectoryUserGetNotFound(t *testing.T) {
	srv, _ := mockDirectory(t)
	cs := connectDirectory(t, srv)

	msg := callToolErr(t, cs, "directory_get_user", map[string]any{"userKey": "missing@example.com"})
	if !strings.Contains(msg, "Not Found") {
		t.Errorf("error = %q", msg)
	}
}

func TestDirectoryGroupsSearchByUser(t *testing.T) {
	srv, cap := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_search_groups", map[string]any{"userKey": "ada@example.com"})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.groupsUK != "ada@example.com" {
		t.Errorf("userKey = %q", cap.groupsUK)
	}
}

func TestDirectoryGroupMembers(t *testing.T) {
	srv, _ := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_list_group_members", map[string]any{"groupKey": "eng@example.com"})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	members := out["members"].([]any)
	first := members[0].(map[string]any)
	if first["role"] != "OWNER" {
		t.Errorf("first member role = %v", first["role"])
	}
}

func TestDirectoryRolesList(t *testing.T) {
	srv, _ := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_list_roles", map[string]any{})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	roles := out["roles"].([]any)
	first := roles[0].(map[string]any)
	if first["isSuperAdminRole"] != true {
		t.Errorf("first role = %v, want super admin", first)
	}
}

func TestDirectoryRoleAssignments(t *testing.T) {
	srv, _ := mockDirectory(t)
	cs := connectDirectory(t, srv)

	_, out := callTool(t, cs, "directory_list_role_assignments", map[string]any{"userKey": "1"})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
	assignments := out["assignments"].([]any)
	first := assignments[0].(map[string]any)
	if first["roleId"] != "1001" {
		t.Errorf("first assignment = %v", first)
	}
}

func TestDirectoryRegistrationGatedByAdminFlag(t *testing.T) {
	// Without --admin, directory tools must not be registered.
	cs := connectServer(t, config.Config{
		ClientID:     "id",
		ClientSecret: "secret",
	})
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if strings.HasPrefix(tool.Name, "directory_") {
			t.Errorf("directory tool %q registered without --admin", tool.Name)
		}
	}
}
