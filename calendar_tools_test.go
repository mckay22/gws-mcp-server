package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// calendarCapture records the query/body the mock saw so tests can assert wiring.
type calendarCapture struct {
	mu           sync.Mutex
	singleEvents string
	orderBy      string
	timeMin      string
	timeMax      string
	eventQuery   string
	freeBusyBody string
}

func mockCalendar(t *testing.T) (*httptest.Server, *calendarCapture) {
	t.Helper()
	cap := &calendarCapture{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /calendar/v3/users/me/calendarList", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"items":[
			{"id":"primary","summary":"Ada Lovelace","primary":true,"accessRole":"owner","timeZone":"UTC"},
			{"id":"team@example.com","summary":"Team","accessRole":"reader"}
		]}`)
	})

	mux.HandleFunc("GET /calendar/v3/calendars/{calId}/events", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		q := r.URL.Query()
		cap.singleEvents = q.Get("singleEvents")
		cap.orderBy = q.Get("orderBy")
		cap.timeMin = q.Get("timeMin")
		cap.timeMax = q.Get("timeMax")
		cap.eventQuery = q.Get("q")
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"items":[
			{"id":"ev1","status":"confirmed","summary":"Standup","start":{"dateTime":"2026-07-20T09:00:00Z"},"end":{"dateTime":"2026-07-20T09:15:00Z"},"organizer":{"email":"ada@example.com","self":true},"attendees":[{"email":"grace@example.com","responseStatus":"accepted"}]}
		],"nextPageToken":"evNext"}`)
	})

	mux.HandleFunc("GET /calendar/v3/calendars/{calId}/events/{eventId}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("eventId") == "missing" {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"Not Found","status":"NOT_FOUND"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"id":"ev1","status":"confirmed","summary":"Standup","description":"Daily sync","location":"Meet","start":{"dateTime":"2026-07-20T09:00:00Z"},"end":{"dateTime":"2026-07-20T09:15:00Z"}}`)
	})

	mux.HandleFunc("POST /calendar/v3/freeBusy", func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.freeBusyBody = string(buf)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"calendars":{"primary":{"busy":[{"start":"2026-07-20T09:00:00Z","end":"2026-07-20T09:15:00Z"}]}}}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectCalendar(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, registerCalendarReadTools)
}

func TestListCalendars(t *testing.T) {
	srv, _ := mockCalendar(t)
	cs := connectCalendar(t, srv)

	_, out := callTool(t, cs, "calendar_list_calendars", map[string]any{})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	cals := out["calendars"].([]any)
	first := cals[0].(map[string]any)
	if first["id"] != "primary" || first["primary"] != true {
		t.Errorf("first calendar = %v", first)
	}
}

func TestListEventsDefaultsWindowAndSingleEvents(t *testing.T) {
	srv, cap := mockCalendar(t)
	cs := connectCalendar(t, srv)

	_, out := callTool(t, cs, "calendar_list_events", map[string]any{})
	if out["count"] != float64(1) {
		t.Errorf("count = %v, want 1", out["count"])
	}
	if out["nextPageToken"] != "evNext" {
		t.Errorf("nextPageToken = %v", out["nextPageToken"])
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.singleEvents != "true" {
		t.Errorf("singleEvents = %q, want true", cap.singleEvents)
	}
	if cap.orderBy != "startTime" {
		t.Errorf("orderBy = %q, want startTime", cap.orderBy)
	}
	// A default window must have been supplied (timeMin now, timeMax ~30d later).
	if cap.timeMin == "" || cap.timeMax == "" {
		t.Errorf("expected default time window, got min=%q max=%q", cap.timeMin, cap.timeMax)
	}
	tMin, err := time.Parse(time.RFC3339, cap.timeMin)
	if err != nil {
		t.Fatalf("timeMin not RFC3339: %v", err)
	}
	tMax, _ := time.Parse(time.RFC3339, cap.timeMax)
	if d := tMax.Sub(tMin); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("default window = %v, want ~30 days", d)
	}
}

func TestListEventsRejectsBadTime(t *testing.T) {
	srv, _ := mockCalendar(t)
	cs := connectCalendar(t, srv)

	msg := callToolErr(t, cs, "calendar_list_events", map[string]any{"timeMin": "last tuesday"})
	if !strings.Contains(msg, "RFC3339") {
		t.Errorf("error = %q, want RFC3339 validation", msg)
	}
}

func TestGetEvent(t *testing.T) {
	srv, _ := mockCalendar(t)
	cs := connectCalendar(t, srv)

	_, out := callTool(t, cs, "calendar_get_event", map[string]any{"eventId": "ev1"})
	if out["summary"] != "Standup" || out["description"] != "Daily sync" {
		t.Errorf("event = %v", out)
	}
}

func TestGetEventNotFound(t *testing.T) {
	srv, _ := mockCalendar(t)
	cs := connectCalendar(t, srv)

	msg := callToolErr(t, cs, "calendar_get_event", map[string]any{"eventId": "missing"})
	if !strings.Contains(msg, "Not Found") {
		t.Errorf("error = %q", msg)
	}
}

func TestFreeBusyQuery(t *testing.T) {
	srv, cap := mockCalendar(t)
	cs := connectCalendar(t, srv)

	_, out := callTool(t, cs, "calendar_freebusy", map[string]any{"calendarIds": []any{"primary"}})
	cals := out["calendars"].(map[string]any)
	primary := cals["primary"].(map[string]any)
	busy := primary["busy"].([]any)
	if len(busy) != 1 {
		t.Fatalf("busy intervals = %v, want 1", busy)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.freeBusyBody, `"items":[{"id":"primary"}]`) {
		t.Errorf("freeBusy body = %q, want items with primary", cap.freeBusyBody)
	}
}
