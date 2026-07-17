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

type calWriteCapture struct {
	mu          sync.Mutex
	called      bool
	method      string
	path        string
	sendUpdates string
	body        string
}

func mockCalendarWrite(t *testing.T) (*httptest.Server, *calWriteCapture) {
	t.Helper()
	cap := &calWriteCapture{}
	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.sendUpdates = r.URL.Query().Get("sendUpdates")
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"ev-new","status":"confirmed"}`)
	}
	mux.HandleFunc("POST /calendar/v3/calendars/{cal}/events", handler)
	mux.HandleFunc("PATCH /calendar/v3/calendars/{cal}/events/{ev}", handler)
	mux.HandleFunc("DELETE /calendar/v3/calendars/{cal}/events/{ev}", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectCalendarWrite(t *testing.T, srv *httptest.Server, allowWrites, allowSends bool) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, func(s *mcp.Server, gc *gapi.Client) {
		registerCalendarWriteTools(s, gc, allowWrites, allowSends)
	})
}

// The attendee split IS the gate split: an appointment (no attendees) rides the
// write gate; an event with attendees rides the send gate.
func TestCreateAppointmentRidesWriteGate(t *testing.T) {
	srv, cap := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, true, false) // write open, send closed

	_, out := callTool(t, cs, "create_appointment", map[string]any{
		"summary": "Focus time",
		"start":   "2026-07-20T09:00:00Z",
		"end":     "2026-07-20T10:00:00Z",
	})
	if out["applied"] != true {
		t.Errorf("appointment should apply with the write gate open: %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !cap.called || cap.method != http.MethodPost {
		t.Errorf("recorded %v %s", cap.called, cap.method)
	}
	if cap.sendUpdates != "" {
		t.Errorf("appointment must not send updates, got sendUpdates=%q", cap.sendUpdates)
	}
}

func TestCreateEventWithAttendeesRidesSendGate(t *testing.T) {
	srv, cap := mockCalendarWrite(t)

	// Write gate open, send gate closed → must be a dry-run (attendees would be
	// emailed).
	cs := connectCalendarWrite(t, srv, true, false)
	_, out := callTool(t, cs, "create_event_with_attendees", map[string]any{
		"summary":   "Sync",
		"start":     "2026-07-20T09:00:00Z",
		"end":       "2026-07-20T10:00:00Z",
		"attendees": []any{"grace@example.com"},
	})
	if out["dryRun"] != true {
		t.Errorf("event-with-attendees must be a dry-run when only the write gate is open: %v", out)
	}
	if cap.called {
		t.Error("must not call Google when the send gate is closed")
	}

	// Send gate open → applies, with sendUpdates=all.
	cs2 := connectCalendarWrite(t, srv, false, true)
	_, out2 := callTool(t, cs2, "create_event_with_attendees", map[string]any{
		"summary":   "Sync",
		"start":     "2026-07-20T09:00:00Z",
		"end":       "2026-07-20T10:00:00Z",
		"attendees": []any{"grace@example.com"},
	})
	if out2["applied"] != true {
		t.Errorf("expected applied with send gate open: %v", out2)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.sendUpdates != "all" {
		t.Errorf("sendUpdates = %q, want all", cap.sendUpdates)
	}
	if !strings.Contains(cap.body, "grace@example.com") {
		t.Errorf("body = %q, want attendee", cap.body)
	}
}

func TestCancelEventRidesSendGate(t *testing.T) {
	srv, cap := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, false, true)

	_, out := callTool(t, cs, "cancel_event", map[string]any{"eventId": "ev1"})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodDelete || cap.sendUpdates != "all" {
		t.Errorf("recorded %s sendUpdates=%q", cap.method, cap.sendUpdates)
	}
}

func TestRespondToEventValidatesResponse(t *testing.T) {
	srv, _ := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, false, true)

	msg := callToolErr(t, cs, "respond_to_event", map[string]any{
		"eventId":   "ev1",
		"selfEmail": "ada@example.com",
		"response":  "maybe",
	})
	if !strings.Contains(msg, "accepted") {
		t.Errorf("error = %q, want valid-response guidance", msg)
	}
}

func TestRespondToEventApplies(t *testing.T) {
	srv, cap := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, false, true)

	_, out := callTool(t, cs, "respond_to_event", map[string]any{
		"eventId":   "ev1",
		"selfEmail": "ada@example.com",
		"response":  "Accepted",
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", cap.method)
	}
	if !strings.Contains(cap.body, `"responseStatus":"accepted"`) || !strings.Contains(cap.body, "ada@example.com") {
		t.Errorf("body = %q", cap.body)
	}
}

func TestCreateAppointmentValidatesTime(t *testing.T) {
	srv, _ := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, true, false)

	msg := callToolErr(t, cs, "create_appointment", map[string]any{
		"summary": "x",
		"start":   "not-a-time",
		"end":     "2026-07-20T10:00:00Z",
	})
	if !strings.Contains(msg, "RFC3339") {
		t.Errorf("error = %q, want RFC3339 validation", msg)
	}
}
