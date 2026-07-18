package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fields projections for the Gmail settings reads, matching the projection
// discipline of the other Gmail tools (etag/kind are otherwise fetched and
// thrown away).
const (
	vacationFields = "enableAutoReply,responseSubject,responseBodyPlainText,restrictToContacts,restrictToDomain,startTime,endTime"
	filterFields   = "filter(id,criteria,action)"
	sendAsFields   = "sendAs(sendAsEmail,displayName,isPrimary,isDefault,treatAsAlias,verificationStatus)"
)

// registerGmailSettingsTools installs the powerful-delegated Gmail settings tools
// (vacation responder — the out-of-office analog — filters, and send-as
// aliases). Setting the vacation responder is a reversible write (allowWrites);
// the rest are reads.
func registerGmailSettingsTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerGetVacation(server, gc)
	registerSetVacation(server, gc, allowWrites, allowSends)
	registerListFilters(server, gc)
	registerListSendAs(server, gc)
}

// --- gmail_get_vacation ---

type getVacationInput struct{}

// VacationSettings is the Gmail auto-reply (out-of-office) configuration.
// StartTime/EndTime are RFC3339 here; Gmail carries them as epoch milliseconds,
// which is not a form a reader can act on.
type VacationSettings struct {
	EnableAutoReply       bool   `json:"enableAutoReply"`
	ResponseSubject       string `json:"responseSubject,omitempty"`
	ResponseBodyPlainText string `json:"responseBodyPlainText,omitempty"`
	RestrictToContacts    bool   `json:"restrictToContacts,omitempty"`
	RestrictToDomain      bool   `json:"restrictToDomain,omitempty"`
	StartTime             string `json:"startTime,omitempty" jsonschema:"start of the auto-reply window, RFC3339"`
	EndTime               string `json:"endTime,omitempty" jsonschema:"end of the auto-reply window, RFC3339"`
}

// UnmarshalJSON decodes Gmail's vacation resource, translating its epoch-
// millisecond schedule bounds into RFC3339. Gmail reports them as strings like
// "1751328000000", which tells a reader nothing about when the responder runs.
func (v *VacationSettings) UnmarshalJSON(b []byte) error {
	type raw VacationSettings // shed this method to avoid recursing
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*v = VacationSettings(r)
	v.StartTime = epochMillisToRFC3339(v.StartTime)
	v.EndTime = epochMillisToRFC3339(v.EndTime)
	return nil
}

// epochMillisToRFC3339 converts Gmail's epoch-millisecond timestamp string to
// RFC3339 (UTC). A value that is not an integer is returned unchanged rather than
// discarded — reporting what Google sent beats reporting nothing.
func epochMillisToRFC3339(s string) string {
	ms, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || ms <= 0 {
		return s
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// rfc3339ToEpochMillis converts an RFC3339 instant to the epoch-millisecond
// string Gmail's vacation resource expects.
func rfc3339ToEpochMillis(s string) (string, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("must be a valid RFC3339 time: %q", s)
	}
	return strconv.FormatInt(t.UnixMilli(), 10), nil
}

func registerGetVacation(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_get_vacation",
		Annotations: readAnnotations(),
		Title:       "Get vacation responder",
		Description: "Get the signed-in user's Gmail vacation responder (out-of-office auto-reply) settings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ getVacationInput) (*mcp.CallToolResult, VacationSettings, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/vacation", url.Values{"fields": {vacationFields}})
		if err != nil {
			return nil, VacationSettings{}, toolError(err)
		}
		var v VacationSettings
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, VacationSettings{}, fmt.Errorf("decoding vacation: %w", err)
		}
		state := "off"
		if v.EnableAutoReply {
			state = "on"
		}
		return text(fmt.Sprintf("vacation responder %s", state)), v, nil
	})
}

// --- gmail_set_vacation (write gate) ---

type setVacationInput struct {
	Enable  bool   `json:"enable" jsonschema:"true to enable the auto-reply, false to disable"`
	Subject string `json:"subject,omitempty" jsonschema:"auto-reply subject; omit to keep the stored one"`
	Body    string `json:"body,omitempty" jsonschema:"auto-reply plain-text body; omit to keep the stored one"`
	// Pointers so an omitted flag is distinguishable from an explicit false —
	// otherwise every call would silently clear these.
	RestrictToContacts *bool  `json:"restrictToContacts,omitempty" jsonschema:"only auto-reply to people in the user's contacts; omit to keep the stored setting"`
	RestrictToDomain   *bool  `json:"restrictToDomain,omitempty" jsonschema:"only auto-reply to people in the user's domain; omit to keep the stored setting"`
	StartTime          string `json:"startTime,omitempty" jsonschema:"RFC3339 start of the auto-reply window; omit to keep the stored one"`
	EndTime            string `json:"endTime,omitempty" jsonschema:"RFC3339 end of the auto-reply window; omit to keep the stored one"`
}

func registerSetVacation(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_set_vacation",
		Annotations: destructiveAnnotations(),
		Title:       "Set vacation responder",
		Description: "Enable or disable the signed-in user's Gmail vacation responder (out-of-office), optionally setting the subject, body, and RFC3339 schedule window. Anything omitted keeps its stored value, so disabling does not erase the message. Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview instead of applying.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setVacationInput) (*mcp.CallToolResult, writeOutput, error) {
		// Validate before planning so a bad time is rejected in dry-run too.
		for name, v := range map[string]string{"startTime": in.StartTime, "endTime": in.EndTime} {
			if s := strings.TrimSpace(v); s != "" {
				if _, err := rfc3339ToEpochMillis(s); err != nil {
					return nil, writeOutput{}, fmt.Errorf("%s %w", name, err)
				}
			}
		}

		verb := "disable"
		if in.Enable {
			verb = "enable"
		}
		plan := writePlan{
			Summary: fmt.Sprintf("%s vacation responder", verb),
			Gate:    gateWrites,
			Method:  "PUT",
			Base:    gapi.BaseGmail,
			Path:    "/users/me/settings/vacation",
			Body:    vacationIntent(in),
			// Gmail's vacation endpoint is a PUT: it REPLACES the whole resource, so
			// sending only the fields this call names would blank the rest —
			// disabling the responder would erase the stored subject, body, and
			// schedule, which the caller never asked for. Read the current settings
			// and overlay only what was actually specified. Runs on apply only, so a
			// dry run still calls nothing.
			Prepare: func(ctx context.Context) (any, error) {
				return mergedVacationBody(ctx, gc, in)
			},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- gmail_list_filters ---

type listFiltersInput struct{}

// MailFilter is a compact Gmail filter (the inbox-rules analog).
type MailFilter struct {
	ID       string         `json:"id"`
	Criteria map[string]any `json:"criteria,omitempty"`
	Action   map[string]any `json:"action,omitempty"`
}

type listFiltersOutput struct {
	Filters []MailFilter `json:"filters"`
	Count   int          `json:"count"`
}

func registerListFilters(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_filters",
		Annotations: readAnnotations(),
		Title:       "List Gmail filters",
		Description: "List the signed-in user's Gmail filters (the inbox-rules analog): each filter's matching criteria and the action it applies.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listFiltersInput) (*mcp.CallToolResult, listFiltersOutput, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/filters", url.Values{"fields": {filterFields}})
		if err != nil {
			return nil, listFiltersOutput{}, toolError(err)
		}
		var env struct {
			Filter []MailFilter `json:"filter"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listFiltersOutput{}, fmt.Errorf("decoding filters: %w", err)
		}
		out := listFiltersOutput{Filters: env.Filter, Count: len(env.Filter)}
		return text(fmt.Sprintf("%d filters", out.Count)), out, nil
	})
}

// --- gmail_list_send_as ---

type listSendAsInput struct{}

// SendAsAlias is a Gmail send-as identity.
type SendAsAlias struct {
	SendAsEmail        string `json:"sendAsEmail"`
	DisplayName        string `json:"displayName,omitempty"`
	IsPrimary          bool   `json:"isPrimary,omitempty"`
	IsDefault          bool   `json:"isDefault,omitempty"`
	TreatAsAlias       bool   `json:"treatAsAlias,omitempty"`
	VerificationStatus string `json:"verificationStatus,omitempty"`
}

type listSendAsOutput struct {
	SendAs []SendAsAlias `json:"sendAs"`
	Count  int           `json:"count"`
}

func registerListSendAs(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_list_send_as",
		Annotations: readAnnotations(),
		Title:       "List send-as addresses",
		Description: "List the signed-in user's Gmail send-as addresses (the primary address and any configured aliases), with their display names and verification status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listSendAsInput) (*mcp.CallToolResult, listSendAsOutput, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/sendAs", url.Values{"fields": {sendAsFields}})
		if err != nil {
			return nil, listSendAsOutput{}, toolError(err)
		}
		var env struct {
			SendAs []SendAsAlias `json:"sendAs"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listSendAsOutput{}, fmt.Errorf("decoding send-as: %w", err)
		}
		out := listSendAsOutput{SendAs: env.SendAs, Count: len(env.SendAs)}
		return text(fmt.Sprintf("%d send-as addresses", out.Count)), out, nil
	})
}

// vacationIntent is the readable description of a set_vacation call, shown in the
// dry-run preview: only the fields the caller actually specified, since the rest
// are carried over from the stored settings at apply time.
func vacationIntent(in setVacationInput) map[string]any {
	body := map[string]any{"enableAutoReply": in.Enable}
	if s := strings.TrimSpace(in.Subject); s != "" {
		body["responseSubject"] = s
	}
	if s := strings.TrimSpace(in.Body); s != "" {
		body["responseBodyPlainText"] = s
	}
	if in.RestrictToContacts != nil {
		body["restrictToContacts"] = *in.RestrictToContacts
	}
	if in.RestrictToDomain != nil {
		body["restrictToDomain"] = *in.RestrictToDomain
	}
	if s := strings.TrimSpace(in.StartTime); s != "" {
		body["startTime"] = s
	}
	if s := strings.TrimSpace(in.EndTime); s != "" {
		body["endTime"] = s
	}
	body["unspecifiedFields"] = "kept as currently stored"
	return body
}

// mergedVacationBody reads the stored vacation settings and overlays the fields
// this call specified, producing the full resource Gmail's PUT expects. Times go
// back out as the epoch milliseconds Gmail wants.
func mergedVacationBody(ctx context.Context, gc *gapi.Client, in setVacationInput) (any, error) {
	raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/vacation", url.Values{"fields": {vacationFields}})
	if err != nil {
		return nil, toolError(err)
	}
	var cur VacationSettings
	if err := json.Unmarshal(raw, &cur); err != nil {
		return nil, fmt.Errorf("decoding current vacation settings: %w", err)
	}

	body := map[string]any{
		"enableAutoReply":       in.Enable,
		"responseSubject":       cur.ResponseSubject,
		"responseBodyPlainText": cur.ResponseBodyPlainText,
		"restrictToContacts":    cur.RestrictToContacts,
		"restrictToDomain":      cur.RestrictToDomain,
	}
	if s := strings.TrimSpace(in.Subject); s != "" {
		body["responseSubject"] = s
	}
	if s := strings.TrimSpace(in.Body); s != "" {
		body["responseBodyPlainText"] = s
	}
	if in.RestrictToContacts != nil {
		body["restrictToContacts"] = *in.RestrictToContacts
	}
	if in.RestrictToDomain != nil {
		body["restrictToDomain"] = *in.RestrictToDomain
	}

	// Schedule bounds: carry the stored window unless this call replaces it.
	// cur.* are RFC3339 (UnmarshalJSON converted them), so both paths re-encode.
	if err := setVacationTime(body, "startTime", in.StartTime, cur.StartTime); err != nil {
		return nil, err
	}
	if err := setVacationTime(body, "endTime", in.EndTime, cur.EndTime); err != nil {
		return nil, err
	}
	return body, nil
}

// setVacationTime writes one schedule bound into the PUT body as epoch
// milliseconds, preferring the value this call specified and otherwise carrying
// the stored one forward. An absent bound is left out entirely.
func setVacationTime(body map[string]any, key, specified, stored string) error {
	v := strings.TrimSpace(specified)
	if v == "" {
		v = strings.TrimSpace(stored)
	}
	if v == "" {
		return nil
	}
	ms, err := rfc3339ToEpochMillis(v)
	if err != nil {
		return fmt.Errorf("%s %w", key, err)
	}
	body[key] = ms
	return nil
}
