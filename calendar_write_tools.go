package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCalendarWriteTools installs the M3 gated Calendar mutation tools. The
// gate split follows the attendee split (verbatim from entra-mcp-server):
// creating an appointment with NO attendees notifies no one and is a reversible
// write (allowWrites); creating/updating/cancelling an event WITH attendees, or
// responding to an invite, sends email to those attendees and rides the SEPARATE
// send gate (allowSends).
func registerCalendarWriteTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerCreateAppointment(server, gc, allowWrites, allowSends)
	registerCreateEventWithAttendees(server, gc, allowWrites, allowSends)
	registerUpdateEvent(server, gc, allowWrites, allowSends)
	registerCancelEvent(server, gc, allowWrites, allowSends)
	registerRespondToEvent(server, gc, allowWrites, allowSends)
}

// eventTimeBody builds a Calendar start/end object from an RFC3339 dateTime.
func eventTimeBody(dateTime string) map[string]any {
	return map[string]any{"dateTime": dateTime}
}

// attendeesBody maps plain addresses to Calendar's attendee shape.
func attendeesBody(addrs []string) []map[string]any {
	out := make([]map[string]any, 0, len(addrs))
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, map[string]any{"email": a})
		}
	}
	return out
}

// --- create_appointment (write gate; no attendees) ---

type createAppointmentInput struct {
	CalendarID  string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	Summary     string `json:"summary" jsonschema:"event title (required)"`
	Start       string `json:"start" jsonschema:"start time, RFC3339 (required)"`
	End         string `json:"end" jsonschema:"end time, RFC3339 (required)"`
	Location    string `json:"location,omitempty" jsonschema:"optional location"`
	Description string `json:"description,omitempty" jsonschema:"optional description"`
}

func registerCreateAppointment(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_appointment",
		Annotations: additiveAnnotations(),
		Title:       "Create appointment (no attendees)",
		Description: "Create a personal calendar event with NO attendees — nobody is emailed, so this is a reversible write gated by " + config.EnvAllowWrites + " (or --allow-writes). To invite attendees use create_event_with_attendees, which is send-gated.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createAppointmentInput) (*mcp.CallToolResult, writeOutput, error) {
		if err := validateEventTimes(in.Summary, in.Start, in.End); err != nil {
			return nil, writeOutput{}, err
		}
		body := map[string]any{
			"summary": in.Summary,
			"start":   eventTimeBody(in.Start),
			"end":     eventTimeBody(in.End),
		}
		addOptional(body, in.Location, in.Description)
		plan := writePlan{
			Summary: fmt.Sprintf("create appointment %q (%s–%s)", in.Summary, in.Start, in.End),
			Gate:    gateWrites,
			Method:  "POST",
			Base:    gapi.BaseCalendar,
			Path:    "/calendars/" + url.PathEscape(calendarOrPrimary(in.CalendarID)) + "/events",
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- create_event_with_attendees (send gate) ---

type createEventWithAttendeesInput struct {
	CalendarID  string   `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	Summary     string   `json:"summary" jsonschema:"event title (required)"`
	Start       string   `json:"start" jsonschema:"start time, RFC3339 (required)"`
	End         string   `json:"end" jsonschema:"end time, RFC3339 (required)"`
	Attendees   []string `json:"attendees" jsonschema:"attendee email addresses to invite (required — they will be emailed)"`
	Location    string   `json:"location,omitempty" jsonschema:"optional location"`
	Description string   `json:"description,omitempty" jsonschema:"optional description"`
}

func registerCreateEventWithAttendees(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_event_with_attendees",
		Annotations: additiveAnnotations(),
		Title:       "Create event with attendees (sends invites)",
		Description: "Create a calendar event WITH attendees and email them invitations (sendUpdates=all). Because it notifies people, it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview of the exact invite instead of sending.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in createEventWithAttendeesInput) (*mcp.CallToolResult, writeOutput, error) {
		if err := validateEventTimes(in.Summary, in.Start, in.End); err != nil {
			return nil, writeOutput{}, err
		}
		if len(in.Attendees) == 0 {
			return nil, writeOutput{}, fmt.Errorf("attendees is required (use create_appointment for an event with no attendees)")
		}
		body := map[string]any{
			"summary":   in.Summary,
			"start":     eventTimeBody(in.Start),
			"end":       eventTimeBody(in.End),
			"attendees": attendeesBody(in.Attendees),
		}
		addOptional(body, in.Location, in.Description)
		plan := writePlan{
			Summary: fmt.Sprintf("create event %q inviting %s", in.Summary, strings.Join(in.Attendees, ", ")),
			Gate:    gateSends,
			Method:  "POST",
			Base:    gapi.BaseCalendar,
			Path:    "/calendars/" + url.PathEscape(calendarOrPrimary(in.CalendarID)) + "/events",
			Query:   url.Values{"sendUpdates": {"all"}},
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- update_event (send gate) ---

type updateEventInput struct {
	CalendarID  string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	EventID     string `json:"eventId" jsonschema:"the event id to update (required)"`
	Summary     string `json:"summary,omitempty" jsonschema:"new title"`
	Start       string `json:"start,omitempty" jsonschema:"new start time, RFC3339"`
	End         string `json:"end,omitempty" jsonschema:"new end time, RFC3339"`
	Location    string `json:"location,omitempty" jsonschema:"new location"`
	Description string `json:"description,omitempty" jsonschema:"new description"`
}

func registerUpdateEvent(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_event",
		Annotations: destructiveAnnotations(),
		Title:       "Update event (notifies attendees)",
		Description: "Patch fields of an existing event and notify its attendees of the change (PATCH, sendUpdates=all). Because it emails attendees it is send-gated: without " + config.EnvAllowSends + "=true it returns a dry-run preview of the patch.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateEventInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.EventID) == "" {
			return nil, writeOutput{}, fmt.Errorf("eventId is required")
		}
		body := map[string]any{}
		if s := strings.TrimSpace(in.Summary); s != "" {
			body["summary"] = s
		}
		if s := strings.TrimSpace(in.Start); s != "" {
			if !validRFC3339(s) {
				return nil, writeOutput{}, fmt.Errorf("start must be a valid RFC3339 time")
			}
			body["start"] = eventTimeBody(s)
		}
		if s := strings.TrimSpace(in.End); s != "" {
			if !validRFC3339(s) {
				return nil, writeOutput{}, fmt.Errorf("end must be a valid RFC3339 time")
			}
			body["end"] = eventTimeBody(s)
		}
		addOptional(body, in.Location, in.Description)
		if len(body) == 0 {
			return nil, writeOutput{}, fmt.Errorf("provide at least one field to update")
		}
		plan := writePlan{
			Summary: fmt.Sprintf("update event %s", in.EventID),
			Gate:    gateSends,
			Method:  "PATCH",
			Base:    gapi.BaseCalendar,
			Path:    "/calendars/" + url.PathEscape(calendarOrPrimary(in.CalendarID)) + "/events/" + url.PathEscape(in.EventID),
			Query:   url.Values{"sendUpdates": {"all"}},
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- cancel_event (send gate) ---

type cancelEventInput struct {
	CalendarID string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	EventID    string `json:"eventId" jsonschema:"the event id to cancel/delete (required)"`
}

func registerCancelEvent(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "cancel_event",
		Annotations: destructiveAnnotations(),
		Title:       "Cancel event (notifies attendees)",
		Description: "Delete/cancel an event and notify its attendees (DELETE, sendUpdates=all). Irreversible and attendee-notifying, so it is send-gated: without " + config.EnvAllowSends + "=true it returns a dry-run preview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in cancelEventInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.EventID) == "" {
			return nil, writeOutput{}, fmt.Errorf("eventId is required")
		}
		plan := writePlan{
			Summary: fmt.Sprintf("cancel event %s", in.EventID),
			Gate:    gateSends,
			Method:  "DELETE",
			Base:    gapi.BaseCalendar,
			Path:    "/calendars/" + url.PathEscape(calendarOrPrimary(in.CalendarID)) + "/events/" + url.PathEscape(in.EventID),
			Query:   url.Values{"sendUpdates": {"all"}},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- respond_to_event (send gate: RSVP notifies the organizer) ---

type respondToEventInput struct {
	CalendarID string `json:"calendarId,omitempty" jsonschema:"calendar id (default 'primary')"`
	EventID    string `json:"eventId" jsonschema:"the event id to RSVP to (required)"`
	Response   string `json:"response" jsonschema:"one of accepted, declined, tentative (required)"`
	SelfEmail  string `json:"selfEmail" jsonschema:"the signed-in user's attendee email on the event (required — the attendee whose responseStatus is set)"`
}

func registerRespondToEvent(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "respond_to_event",
		Annotations: destructiveAnnotations(),
		Title:       "RSVP to an event",
		Description: "Set the signed-in user's RSVP (accepted/declined/tentative) on an event and notify the organizer (PATCH the matching attendee, sendUpdates=all). Because it emails the organizer it is send-gated: without " + config.EnvAllowSends + "=true it returns a dry-run preview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in respondToEventInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.EventID) == "" || strings.TrimSpace(in.SelfEmail) == "" {
			return nil, writeOutput{}, fmt.Errorf("eventId and selfEmail are required")
		}
		resp := strings.ToLower(strings.TrimSpace(in.Response))
		switch resp {
		case "accepted", "declined", "tentative":
		default:
			return nil, writeOutput{}, fmt.Errorf("response must be accepted, declined, or tentative, got %q", in.Response)
		}
		// A PATCH replaces the attendees array, so we send only the responding
		// attendee with the new status; Calendar merges by email.
		body := map[string]any{
			"attendees": []map[string]any{
				{"email": in.SelfEmail, "responseStatus": resp},
			},
		}
		plan := writePlan{
			Summary: fmt.Sprintf("RSVP %s to event %s as %s", resp, in.EventID, in.SelfEmail),
			Gate:    gateSends,
			Method:  "PATCH",
			Base:    gapi.BaseCalendar,
			Path:    "/calendars/" + url.PathEscape(calendarOrPrimary(in.CalendarID)) + "/events/" + url.PathEscape(in.EventID),
			Query:   url.Values{"sendUpdates": {"all"}},
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- helpers ---

// validateEventTimes checks a create's required summary and RFC3339 bounds.
func validateEventTimes(summary, start, end string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if !validRFC3339(start) {
		return fmt.Errorf("start must be a valid RFC3339 time")
	}
	if !validRFC3339(end) {
		return fmt.Errorf("end must be a valid RFC3339 time")
	}
	return nil
}

// addOptional adds location/description to an event body when non-blank.
func addOptional(body map[string]any, location, description string) {
	if s := strings.TrimSpace(location); s != "" {
		body["location"] = s
	}
	if s := strings.TrimSpace(description); s != "" {
		body["description"] = s
	}
}
