package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type powerfulCapture struct {
	mu     sync.Mutex
	called bool
	method string
	path   string
	body   string
	query  string
}

func mockPowerful(t *testing.T) (*httptest.Server, *powerfulCapture) {
	t.Helper()
	cap := &powerfulCapture{}
	record := func(r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body = string(b)
		cap.query = r.URL.RawQuery
		cap.mu.Unlock()
	}
	mux := http.NewServeMux()

	// Gmail settings.
	mux.HandleFunc("GET /gmail/v1/users/me/settings/vacation", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"enableAutoReply":true,"responseSubject":"OOO","responseBodyPlainText":"Away until Monday"}`)
	})
	mux.HandleFunc("PUT /gmail/v1/users/me/settings/vacation", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"enableAutoReply":true}`)
	})
	mux.HandleFunc("GET /gmail/v1/users/me/settings/filters", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"filter":[{"id":"f1","criteria":{"from":"noreply@x.com"},"action":{"addLabelIds":["TRASH"]}}]}`)
	})
	mux.HandleFunc("GET /gmail/v1/users/me/settings/sendAs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"sendAs":[{"sendAsEmail":"ada@example.com","isPrimary":true},{"sendAsEmail":"ada.alias@example.com","treatAsAlias":true}]}`)
	})

	// Tasks.
	mux.HandleFunc("GET /tasks/v1/users/@me/lists", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[{"id":"list1","title":"My Tasks"}]}`)
	})
	mux.HandleFunc("GET /tasks/v1/lists/{list}/tasks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[{"id":"t1","title":"Buy milk","status":"needsAction"}]}`)
	})
	mux.HandleFunc("POST /tasks/v1/lists/{list}/tasks", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"id":"t2","title":"New"}`)
	})
	mux.HandleFunc("PATCH /tasks/v1/lists/{list}/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"id":"t1","status":"completed"}`)
	})

	// People.
	mux.HandleFunc("GET /v1/people:searchContacts", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"results":[{"person":{"resourceName":"people/c1","names":[{"displayName":"Grace Hopper"}],"emailAddresses":[{"value":"grace@example.com"}]}}]}`)
	})

	// Chat.
	mux.HandleFunc("GET /v1/spaces", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"spaces":[{"name":"spaces/AAAA","displayName":"Team","spaceType":"SPACE"}]}`)
	})
	mux.HandleFunc("GET /v1/spaces/{space}/messages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"messages":[{"name":"spaces/AAAA/messages/1","text":"hi"}]}`)
	})
	mux.HandleFunc("POST /v1/spaces/{space}/messages", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"name":"spaces/AAAA/messages/2","text":"posted"}`)
	})

	// Meet.
	mux.HandleFunc("GET /v2/conferenceRecords", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"conferenceRecords":[{"name":"conferenceRecords/abc","startTime":"2026-07-10T09:00:00Z"}]}`)
	})

	// Drive shared-with-me.
	mux.HandleFunc("GET /drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		writeJSON(w, http.StatusOK, `{"files":[{"id":"shared1","name":"Shared Doc","shared":true}]}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectPowerful(t *testing.T, srv *httptest.Server, allowWrites, allowSends bool) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, func(s *mcp.Server, gc *gapi.Client) {
		registerPowerfulTools(s, gc, allowWrites, allowSends)
	})
}

func TestGetVacation(t *testing.T) {
	srv, _ := mockPowerful(t)
	cs := connectPowerful(t, srv, false, false)

	_, out := callTool(t, cs, "gmail_get_vacation", map[string]any{})
	if out["enableAutoReply"] != true || out["responseSubject"] != "OOO" {
		t.Errorf("vacation = %v", out)
	}
}

func TestSetVacationRidesWriteGate(t *testing.T) {
	srv, cap := mockPowerful(t)

	// Gate closed → dry-run.
	cs := connectPowerful(t, srv, false, false)
	_, out := callTool(t, cs, "gmail_set_vacation", map[string]any{"enable": true, "subject": "OOO", "body": "away"})
	if out["dryRun"] != true {
		t.Errorf("expected dry-run, got %v", out)
	}
	if cap.called {
		t.Error("dry-run must not call the API")
	}

	// Gate open → PUT applied.
	cs2 := connectPowerful(t, srv, true, false)
	_, out2 := callTool(t, cs2, "gmail_set_vacation", map[string]any{"enable": true, "subject": "OOO", "body": "away"})
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodPut || !strings.Contains(cap.body, "enableAutoReply") {
		t.Errorf("recorded %s body=%q", cap.method, cap.body)
	}
}

func TestListFiltersAndSendAs(t *testing.T) {
	srv, _ := mockPowerful(t)
	cs := connectPowerful(t, srv, false, false)

	_, filters := callTool(t, cs, "gmail_list_filters", map[string]any{})
	if filters["count"] != float64(1) {
		t.Errorf("filters count = %v", filters["count"])
	}
	_, sendAs := callTool(t, cs, "gmail_list_send_as", map[string]any{})
	if sendAs["count"] != float64(2) {
		t.Errorf("sendAs count = %v", sendAs["count"])
	}
}

func TestTasksListAndCreate(t *testing.T) {
	srv, cap := mockPowerful(t)

	cs := connectPowerful(t, srv, false, false)
	_, lists := callTool(t, cs, "tasks_list_tasklists", map[string]any{})
	if lists["count"] != float64(1) {
		t.Errorf("tasklists count = %v", lists["count"])
	}
	_, tasks := callTool(t, cs, "tasks_list", map[string]any{})
	if tasks["count"] != float64(1) {
		t.Errorf("tasks count = %v", tasks["count"])
	}

	// create is write-gated.
	_, dry := callTool(t, cs, "tasks_create", map[string]any{"title": "New"})
	if dry["dryRun"] != true {
		t.Errorf("tasks_create should dry-run with gate closed: %v", dry)
	}

	cs2 := connectPowerful(t, srv, true, false)
	_, applied := callTool(t, cs2, "tasks_create", map[string]any{"title": "New"})
	if applied["applied"] != true {
		t.Errorf("expected applied, got %v", applied)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.body, "New") {
		t.Errorf("create body = %q", cap.body)
	}
}

func TestPeopleSearch(t *testing.T) {
	srv, cap := mockPowerful(t)
	cs := connectPowerful(t, srv, false, false)

	_, out := callTool(t, cs, "people_search_contacts", map[string]any{"query": "grace"})
	if out["count"] != float64(1) {
		t.Errorf("count = %v", out["count"])
	}
	contacts := out["contacts"].([]any)
	first := contacts[0].(map[string]any)
	if first["displayName"] != "Grace Hopper" {
		t.Errorf("contact = %v", first)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.query, "readMask") {
		t.Errorf("query = %q, want readMask", cap.query)
	}
}

func TestChatListAndSend(t *testing.T) {
	srv, cap := mockPowerful(t)

	cs := connectPowerful(t, srv, false, false)
	_, spaces := callTool(t, cs, "chat_list_spaces", map[string]any{})
	if spaces["count"] != float64(1) {
		t.Errorf("spaces count = %v", spaces["count"])
	}
	_, msgs := callTool(t, cs, "chat_list_messages", map[string]any{"space": "spaces/AAAA"})
	if msgs["count"] != float64(1) {
		t.Errorf("messages count = %v", msgs["count"])
	}

	// send is send-gated: write gate open but send gate closed → dry-run.
	cs2 := connectPowerful(t, srv, true, false)
	_, dry := callTool(t, cs2, "chat_send_message", map[string]any{"space": "spaces/AAAA", "text": "hello"})
	if dry["dryRun"] != true {
		t.Errorf("chat_send must dry-run when only the write gate is open: %v", dry)
	}
	if cap.called {
		t.Error("chat_send must not post when the send gate is closed")
	}

	// send gate open → applied.
	cs3 := connectPowerful(t, srv, false, true)
	_, applied := callTool(t, cs3, "chat_send_message", map[string]any{"space": "spaces/AAAA", "text": "hello"})
	if applied["applied"] != true {
		t.Errorf("expected applied, got %v", applied)
	}
}

func TestMeetAndSharedWithMe(t *testing.T) {
	srv, cap := mockPowerful(t)
	cs := connectPowerful(t, srv, false, false)

	_, meet := callTool(t, cs, "meet_conference_records", map[string]any{})
	if meet["count"] != float64(1) {
		t.Errorf("meet count = %v", meet["count"])
	}

	_, shared := callTool(t, cs, "drive_shared_with_me", map[string]any{})
	if shared["count"] != float64(1) {
		t.Errorf("shared count = %v", shared["count"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.query, "sharedWithMe") {
		t.Errorf("shared-with-me query = %q", cap.query)
	}
}
