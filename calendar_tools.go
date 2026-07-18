package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCalendarReadTools installs the M2 read-only Calendar tools. Every tool
// acts as the signed-in user, so Google enforces which calendars and events the
// caller may see.
func registerCalendarReadTools(server *mcp.Server, gc *gapi.Client) {
	registerListCalendars(server, gc)
	registerListEvents(server, gc)
	registerGetEvent(server, gc)
	registerFreeBusy(server, gc)
}

// Default look-ahead windows when the caller omits time bounds.
const (
	defaultEventWindowDays    = 30
	defaultFreeBusyWindowDays = 7
)

// fields projections for Calendar responses.
const (
	calendarListFields = "items(id,summary,description,primary,accessRole,timeZone,selected),nextPageToken"
	eventListFields    = "items(id,status,summary,location,start,end,organizer(email,displayName,self),attendees(email,responseStatus),hangoutLink,htmlLink,recurringEventId),nextPageToken"
	freeBusyFields     = "calendars(busy,errors)"
	eventGetFields     = "id,status,summary,description,location,start,end,organizer(email,displayName,self),attendees(email,responseStatus),hangoutLink,htmlLink,recurringEventId,created,updated"
)

// --- shared shapes ---

// EventTime is a Calendar event boundary: either an all-day date or a timed
// dateTime (RFC3339), plus an optional IANA time zone.
type EventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

// Person is a trimmed organizer/creator reference.
type Person struct {
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Self        bool   `json:"self,omitempty"`
}

// Attendee is a trimmed event attendee with their RSVP state.
type Attendee struct {
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

// Event is the summarized calendar event returned by calendar_list_events/calendar_get_event. Its
// JSON tags double as the decode target for a Calendar event resource.
// Description is populated only by calendar_get_event (list projection omits it).
type Event struct {
	ID               string     `json:"id"`
	Status           string     `json:"status,omitempty"`
	Summary          string     `json:"summary,omitempty"`
	Description      string     `json:"description,omitempty"`
	Location         string     `json:"location,omitempty"`
	Start            EventTime  `json:"start"`
	End              EventTime  `json:"end"`
	Organizer        *Person    `json:"organizer,omitempty"`
	Attendees        []Attendee `json:"attendees,omitempty"`
	HangoutLink      string     `json:"hangoutLink,omitempty"`
	HTMLLink         string     `json:"htmlLink,omitempty"`
	RecurringEventID string     `json:"recurringEventId,omitempty"`
	Created          string     `json:"created,omitempty"`
	Updated          string     `json:"updated,omitempty"`
}

// --- calendar_list_calendars ---

type listCalendarsInput struct {
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
}

// CalendarRef is a compact entry from the user's calendar list.
type CalendarRef struct {
	ID          string `json:"id"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Primary     bool   `json:"primary,omitempty"`
	AccessRole  string `json:"accessRole,omitempty"`
	TimeZone    string `json:"timeZone,omitempty"`
	Selected    bool   `json:"selected,omitempty"`
}

type listCalendarsOutput struct {
	Calendars     []CalendarRef `json:"calendars"`
	Count         int           `json:"count"`
	NextPageToken string        `json:"nextPageToken,omitempty"`
}

func registerListCalendars(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_calendars",
		Annotations: readAnnotations(),
		Title:       "List calendars",
		Description: "List the calendars on the signed-in user's calendar list, with their ids (use as calendarId in calendar_list_events/calendar_get_event), access role, and time zone. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listCalendarsInput) (*mcp.CallToolResult, listCalendarsOutput, error) {
		// One bounded page plus a token, like every other list tool here. This used
		// to auto-follow every page, so a user subscribed to hundreds of calendars
		// got them all in one result with no way to ask for less.
		q := url.Values{
			"fields":     {calendarListFields},
			"maxResults": {strconv.Itoa(clampLimit(in.MaxResults))},
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseCalendar, "/users/me/calendarList", q)
		if err != nil {
			return nil, listCalendarsOutput{}, toolError(err)
		}
		var env struct {
			Items         []CalendarRef `json:"items"`
			NextPageToken string        `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listCalendarsOutput{}, fmt.Errorf("decoding calendars: %w", err)
		}
		cals := env.Items
		if cals == nil {
			cals = []CalendarRef{}
		}
		out := listCalendarsOutput{Calendars: cals, Count: len(cals), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d calendars", out.Count)), out, nil
	})
}

// --- calendar_list_events ---

type listEventsInput struct {
	CalendarID string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary'); get ids from calendar_list_calendars"`
	TimeMin    string `json:"timeMin,omitempty" jsonschema:"window start, RFC3339 (default now)"`
	TimeMax    string `json:"timeMax,omitempty" jsonschema:"window end, RFC3339 (default 30 days after timeMin)"`
	Query      string `json:"query,omitempty" jsonschema:"free-text search over event fields"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
}

type listEventsOutput struct {
	Events        []Event `json:"events"`
	Count         int     `json:"count"`
	TimeMin       string  `json:"timeMin"`
	TimeMax       string  `json:"timeMax"`
	NextPageToken string  `json:"nextPageToken,omitempty"`
}

func registerListEvents(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_list_events",
		Annotations: readAnnotations(),
		Title:       "List calendar events",
		Description: "List events in a calendar within a time window, expanded to single instances and ordered by start time. Defaults to the primary calendar and the next 30 days. Returns one page; page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, listEventsOutput, error) {
		out, err := listEventsFor(ctx, gc, calendarOrPrimary(in.CalendarID), in.TimeMin, in.TimeMax, in.Query, in.MaxResults, in.PageToken)
		if err != nil {
			return nil, listEventsOutput{}, err
		}
		return text(fmt.Sprintf("%d events between %s and %s", out.Count, out.TimeMin, out.TimeMax)), out, nil
	})
}

// listEventsFor lists a windowed, single-instance, start-ordered page of events
// for a calendar. It is shared by calendar_list_events and app_list_events. Blank time
// bounds default (now / +30 days); a malformed bound is an error.
func listEventsFor(ctx context.Context, gc *gapi.Client, calendarID, timeMinIn, timeMaxIn, query string, maxResults int, pageToken string) (listEventsOutput, error) {
	now := time.Now()
	timeMin, err := rfc3339OrDefault(timeMinIn, now)
	if err != nil {
		return listEventsOutput{}, err
	}
	base, _ := time.Parse(time.RFC3339, timeMin)
	timeMax, err := rfc3339OrDefault(timeMaxIn, base.AddDate(0, 0, defaultEventWindowDays))
	if err != nil {
		return listEventsOutput{}, err
	}

	q := url.Values{}
	q.Set("singleEvents", "true")
	q.Set("orderBy", "startTime")
	q.Set("timeMin", timeMin)
	q.Set("timeMax", timeMax)
	q.Set("maxResults", strconv.Itoa(clampLimit(maxResults)))
	q.Set("fields", eventListFields)
	if s := strings.TrimSpace(query); s != "" {
		q.Set("q", s)
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}

	raw, err := gc.Get(ctx, gapi.BaseCalendar, "/calendars/"+url.PathEscape(calendarID)+"/events", q)
	if err != nil {
		return listEventsOutput{}, toolError(err)
	}
	var env struct {
		Items         []Event `json:"items"`
		NextPageToken string  `json:"nextPageToken"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return listEventsOutput{}, fmt.Errorf("decoding events: %w", err)
	}
	return listEventsOutput{
		Events:        env.Items,
		Count:         len(env.Items),
		TimeMin:       timeMin,
		TimeMax:       timeMax,
		NextPageToken: env.NextPageToken,
	}, nil
}

// listPrimaryEvents is the app tier's convenience wrapper: events on the target
// user's primary calendar.
func listPrimaryEvents(ctx context.Context, gc *gapi.Client, timeMin, timeMax string, maxResults int, pageToken string) (listEventsOutput, error) {
	return listEventsFor(ctx, gc, "primary", timeMin, timeMax, "", maxResults, pageToken)
}

// --- calendar_get_event ---

type getEventInput struct {
	CalendarID string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	EventID    string `json:"eventId" jsonschema:"the event id from calendar_list_events"`
}

func registerGetEvent(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_get_event",
		Annotations: readAnnotations(),
		Title:       "Get calendar event",
		Description: "Fetch a single event by id (including its description and attendee RSVP states) from the given calendar (default 'primary').",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getEventInput) (*mcp.CallToolResult, Event, error) {
		if strings.TrimSpace(in.EventID) == "" {
			return nil, Event{}, fmt.Errorf("eventId is required")
		}
		calendarID := calendarOrPrimary(in.CalendarID)
		q := url.Values{"fields": {eventGetFields}}
		raw, err := gc.Get(ctx, gapi.BaseCalendar, "/calendars/"+url.PathEscape(calendarID)+"/events/"+url.PathEscape(in.EventID), q)
		if err != nil {
			return nil, Event{}, toolError(err)
		}
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, Event{}, fmt.Errorf("decoding event: %w", err)
		}
		return text(fmt.Sprintf("%s (%s)", ev.Summary, ev.Status)), ev, nil
	})
}

// --- calendar_freebusy ---

type freeBusyInput struct {
	TimeMin     string   `json:"timeMin,omitempty" jsonschema:"window start, RFC3339 (default now)"`
	TimeMax     string   `json:"timeMax,omitempty" jsonschema:"window end, RFC3339 (default 7 days after timeMin)"`
	CalendarIDs []string `json:"calendarIds,omitempty" jsonschema:"calendar ids to query (default ['primary'])"`
}

// BusyInterval is a single busy block returned by a free/busy query.
type BusyInterval struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// CalendarBusy holds the busy blocks (and any per-calendar errors) for one
// queried calendar.
type CalendarBusy struct {
	Busy   []BusyInterval `json:"busy"`
	Errors []struct {
		Domain string `json:"domain"`
		Reason string `json:"reason"`
	} `json:"errors,omitempty"`
}

type freeBusyOutput struct {
	TimeMin   string                  `json:"timeMin"`
	TimeMax   string                  `json:"timeMax"`
	Calendars map[string]CalendarBusy `json:"calendars"`
}

func registerFreeBusy(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "calendar_freebusy",
		Annotations: readAnnotations(),
		Title:       "Query free/busy",
		Description: "Return busy time intervals for one or more calendars in a window (default the primary calendar, next 7 days) — the free/busy availability view, without event details.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in freeBusyInput) (*mcp.CallToolResult, freeBusyOutput, error) {
		now := time.Now()
		timeMin, err := rfc3339OrDefault(in.TimeMin, now)
		if err != nil {
			return nil, freeBusyOutput{}, err
		}
		base, _ := time.Parse(time.RFC3339, timeMin)
		timeMax, err := rfc3339OrDefault(in.TimeMax, base.AddDate(0, 0, defaultFreeBusyWindowDays))
		if err != nil {
			return nil, freeBusyOutput{}, err
		}

		ids := in.CalendarIDs
		if len(ids) == 0 {
			ids = []string{"primary"}
		}
		items := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				items = append(items, map[string]string{"id": id})
			}
		}

		body := map[string]any{"timeMin": timeMin, "timeMax": timeMax, "items": items}
		raw, err := gc.Post(ctx, gapi.BaseCalendar, "/freeBusy",
			url.Values{"fields": {freeBusyFields}}, body)
		if err != nil {
			return nil, freeBusyOutput{}, toolError(err)
		}
		var env struct {
			Calendars map[string]CalendarBusy `json:"calendars"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, freeBusyOutput{}, fmt.Errorf("decoding free/busy: %w", err)
		}
		out := freeBusyOutput{TimeMin: timeMin, TimeMax: timeMax, Calendars: env.Calendars}
		return text(fmt.Sprintf("free/busy for %d calendars between %s and %s", len(out.Calendars), timeMin, timeMax)), out, nil
	})
}

// --- helpers ---

// calendarOrPrimary returns a trimmed calendar id, defaulting to "primary".
func calendarOrPrimary(id string) string {
	if s := strings.TrimSpace(id); s != "" {
		return s
	}
	return "primary"
}

// validRFC3339 reports whether s parses as an RFC3339 timestamp.
func validRFC3339(s string) bool {
	_, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	return err == nil
}

// rfc3339OrDefault returns v when it is a valid RFC3339 timestamp, def (UTC,
// RFC3339) when v is blank, or an error when v is present but malformed.
func rfc3339OrDefault(v string, def time.Time) (string, error) {
	if strings.TrimSpace(v) == "" {
		return def.UTC().Format(time.RFC3339), nil
	}
	s := strings.TrimSpace(v)
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return "", fmt.Errorf("invalid RFC3339 time %q", v)
	}
	return s, nil
}
