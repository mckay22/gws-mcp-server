package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
type VacationSettings struct {
	EnableAutoReply       bool   `json:"enableAutoReply"`
	ResponseSubject       string `json:"responseSubject,omitempty"`
	ResponseBodyPlainText string `json:"responseBodyPlainText,omitempty"`
	RestrictToContacts    bool   `json:"restrictToContacts,omitempty"`
	RestrictToDomain      bool   `json:"restrictToDomain,omitempty"`
	StartTime             string `json:"startTime,omitempty"`
	EndTime               string `json:"endTime,omitempty"`
}

func registerGetVacation(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_get_vacation",
		Title:       "Get vacation responder",
		Description: "Get the signed-in user's Gmail vacation responder (out-of-office auto-reply) settings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ getVacationInput) (*mcp.CallToolResult, VacationSettings, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/vacation", nil)
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
	Enable             bool   `json:"enable" jsonschema:"true to enable the auto-reply, false to disable"`
	Subject            string `json:"subject,omitempty" jsonschema:"auto-reply subject (used when enabling)"`
	Body               string `json:"body,omitempty" jsonschema:"auto-reply plain-text body (used when enabling)"`
	RestrictToContacts bool   `json:"restrictToContacts,omitempty" jsonschema:"only auto-reply to people in the user's contacts"`
	RestrictToDomain   bool   `json:"restrictToDomain,omitempty" jsonschema:"only auto-reply to people in the user's domain"`
}

func registerSetVacation(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_set_vacation",
		Title:       "Set vacation responder",
		Description: "Enable or disable the signed-in user's Gmail vacation responder (out-of-office). Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview instead of applying.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setVacationInput) (*mcp.CallToolResult, writeOutput, error) {
		body := map[string]any{"enableAutoReply": in.Enable}
		if in.Enable {
			if s := strings.TrimSpace(in.Subject); s != "" {
				body["responseSubject"] = s
			}
			if s := strings.TrimSpace(in.Body); s != "" {
				body["responseBodyPlainText"] = s
			}
			if in.RestrictToContacts {
				body["restrictToContacts"] = true
			}
			if in.RestrictToDomain {
				body["restrictToDomain"] = true
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
			Body:    body,
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
		Title:       "List Gmail filters",
		Description: "List the signed-in user's Gmail filters (the inbox-rules analog): each filter's matching criteria and the action it applies.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listFiltersInput) (*mcp.CallToolResult, listFiltersOutput, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/filters", nil)
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
		Title:       "List send-as addresses",
		Description: "List the signed-in user's Gmail send-as addresses (the primary address and any configured aliases), with their display names and verification status.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listSendAsInput) (*mcp.CallToolResult, listSendAsOutput, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/settings/sendAs", nil)
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
