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
	// reads counts event GETs, which the RSVP read-modify-write performs before
	// its PATCH. Tracked separately so a dry run can be asserted to make no call
	// of any kind.
	reads int
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
	// The event read behind the RSVP read-modify-write. Three attendees, so a
	// PATCH that fails to preserve them is visible.
	mux.HandleFunc("GET /calendar/v3/calendars/{cal}/events/{ev}", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		cap.reads++
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"attendees":[
			{"email":"ada@example.com","responseStatus":"needsAction","self":true},
			{"email":"grace@example.com","responseStatus":"accepted"},
			{"email":"alan@example.com","responseStatus":"declined","comment":"conflict","additionalGuests":2}
		]}`)
	})
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

// TestRespondToEventPreservesOtherAttendees is the regression test for the
// attendee-wipe bug. Calendar's PATCH overwrites array fields wholesale, so an
// RSVP that sends only the responding attendee silently removes everyone else
// from the event. The RSVP must read the current list and send it back with only
// its own responseStatus changed.
func TestRespondToEventPreservesOtherAttendees(t *testing.T) {
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
	if cap.reads != 1 {
		t.Errorf("event reads = %d, want 1 (the RSVP must read the attendee list first)", cap.reads)
	}
	if !strings.Contains(cap.body, `"email":"ada@example.com","responseStatus":"accepted"`) &&
		!strings.Contains(cap.body, `"responseStatus":"accepted","email":"ada@example.com"`) {
		t.Errorf("responder's status not set to accepted; body = %q", cap.body)
	}
	// The other attendees must still be there, with their own status intact.
	for _, want := range []string{"grace@example.com", "alan@example.com"} {
		if !strings.Contains(cap.body, want) {
			t.Errorf("PATCH dropped attendee %s — the event would lose them; body = %q", want, cap.body)
		}
	}
	if !strings.Contains(cap.body, `"responseStatus":"declined"`) {
		t.Errorf("another attendee's responseStatus was not preserved; body = %q", cap.body)
	}
	// Per-attendee fields this server does not model must survive the round trip.
	if !strings.Contains(cap.body, "conflict") || !strings.Contains(cap.body, "additionalGuests") {
		t.Errorf("unmodeled attendee fields were dropped; body = %q", cap.body)
	}
}

// A closed gate must make NO Google call at all — not even the read behind the
// read-modify-write.
func TestRespondToEventDryRunCallsNothing(t *testing.T) {
	srv, cap := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, true, false) // write open, send CLOSED

	_, out := callTool(t, cs, "respond_to_event", map[string]any{
		"eventId":   "ev1",
		"selfEmail": "ada@example.com",
		"response":  "accepted",
	})
	if out["dryRun"] != true || out["applied"] == true {
		t.Errorf("expected a dry run, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.called || cap.reads != 0 {
		t.Errorf("dry run called Google (mutated=%v reads=%d)", cap.called, cap.reads)
	}
}

// RSVP-ing as someone who is not on the event must fail loudly rather than PATCH
// a one-element attendee list over the real one.
func TestRespondToEventRefusesNonAttendee(t *testing.T) {
	srv, cap := mockCalendarWrite(t)
	cs := connectCalendarWrite(t, srv, false, true)

	msg := callToolErr(t, cs, "respond_to_event", map[string]any{
		"eventId":   "ev1",
		"selfEmail": "stranger@example.com",
		"response":  "accepted",
	})
	if !strings.Contains(msg, "not an attendee") {
		t.Errorf("error = %q, want it to explain the address is not an attendee", msg)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.called {
		t.Errorf("a mutation was sent despite the responder not being an attendee: %s %s", cap.method, cap.body)
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
