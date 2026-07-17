package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/mckay22/gws-mcp-server/internal/googleauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerAppTools installs the powerful-application tier (--app-only): tools
// that take a required `user` target and act on ANY principal via the
// application-tier service account's domain-wide delegation. It reuses the DWD
// backend by injecting the target user into each call's context. Every APPLIED
// mutation is logged with the requesting actor (the verified caller in
// resource-server mode, or "local" on stdio) — Google's own audit attributes a
// DWD action to the impersonated user, so this log is where the real requester
// lives. gc is the application tier's OWN client (its own SA), never the
// delegated or resource-server one.
func registerAppTools(server *mcp.Server, gc *gapi.Client, cfg config.Config) {
	registerAppListMessages(server, gc)
	registerAppGetMessage(server, gc)
	registerAppSendMail(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerAppListEvents(server, gc)
	registerAppListFiles(server, gc)
	registerAppSetVacation(server, gc, cfg.AllowWrites, cfg.AllowSends)
	registerAppBulkUserSuspend(server, gc, cfg)
	registerAppBulkGroupAddMembers(server, gc, cfg)
}

// asUser returns a context that makes the application-tier DWD backend
// impersonate target, while preserving any requesting-actor already on ctx.
func asUser(ctx context.Context, target string) context.Context {
	return googleauth.WithUser(ctx, target)
}

// appApply runs a single-request app-tier mutation and, when it is actually
// applied (not a dry-run), logs the requesting actor against the target and
// action.
func appApply(ctx context.Context, gc *gapi.Client, allowWrites, allowSends bool, target string, plan writePlan) (*mcp.CallToolResult, writeOutput, error) {
	res, out, err := runWrite(asUser(ctx, target), gc, allowWrites, allowSends, plan)
	if err == nil && out.Applied {
		slog.Info("app-tier mutation applied", "actor", actorFromContext(ctx), "target", target, "action", plan.Summary)
	}
	return res, out, err
}

// --- app_list_messages ---

type appListMessagesInput struct {
	User       string   `json:"user" jsonschema:"the target user's email address whose mailbox to list (required)"`
	Query      string   `json:"query,omitempty" jsonschema:"optional Gmail search query"`
	LabelIDs   []string `json:"labelIds,omitempty" jsonschema:"optional label ids to filter by"`
	MaxResults int      `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string   `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

func registerAppListMessages(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_list_messages",
		Title:       "App: list a user's messages",
		Description: "List messages in an explicit user's mailbox via the application service account (domain-wide delegation). Requires the SA's DWD grant to cover Gmail read for that user. Returns message + thread ids; page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appListMessagesInput) (*mcp.CallToolResult, messageListOutput, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, messageListOutput{}, fmt.Errorf("user is required")
		}
		out, err := listMessages(asUser(ctx, in.User), gc, in.User, in.Query, in.LabelIDs, in.MaxResults, in.PageToken, false)
		if err != nil {
			return nil, messageListOutput{}, err
		}
		return text(fmt.Sprintf("%d messages for %s", out.Count, in.User)), out, nil
	})
}

// --- app_get_message ---

type appGetMessageInput struct {
	User   string `json:"user" jsonschema:"the target user's email address (required)"`
	ID     string `json:"id" jsonschema:"the message id (required)"`
	Format string `json:"format,omitempty" jsonschema:"'metadata' (default) or 'full'"`
}

func registerAppGetMessage(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_get_message",
		Title:       "App: get a user's message",
		Description: "Fetch a single message from an explicit user's mailbox via the application service account. 'full' adds the decoded plain-text body (capped at 100 KiB).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appGetMessageInput) (*mcp.CallToolResult, MessageDetail, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, MessageDetail{}, fmt.Errorf("user is required")
		}
		detail, err := fetchMessageDetail(asUser(ctx, in.User), gc, in.User, in.ID, in.Format)
		if err != nil {
			return nil, MessageDetail{}, err
		}
		return text(fmt.Sprintf("%s — %s", in.User, detail.Subject)), detail, nil
	})
}

// --- app_send_mail (send gate) ---

type appSendMailInput struct {
	User    string   `json:"user" jsonschema:"the mailbox to send AS (the message is sent from this user) (required)"`
	To      []string `json:"to" jsonschema:"recipient email addresses (required)"`
	Cc      []string `json:"cc,omitempty" jsonschema:"carbon-copy email addresses"`
	Subject string   `json:"subject" jsonschema:"the subject (required)"`
	Body    string   `json:"body" jsonschema:"the plain-text body (required)"`
}

func registerAppSendMail(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_send_mail",
		Title:       "App: send mail as a user",
		Description: "Send mail AS an explicit user via the application service account. Irreversible, so it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview showing the full message. Applied sends are logged with the requesting actor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appSendMailInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.User) == "" || len(in.To) == 0 || strings.TrimSpace(in.Subject) == "" {
			return nil, writeOutput{}, fmt.Errorf("user, to, and subject are required")
		}
		mimeText, err := buildMIME(in.To, in.Cc, in.Subject, in.Body, "")
		if err != nil {
			return nil, writeOutput{}, err
		}
		preview := readablePreview(in.To, in.Cc, in.Subject, in.Body)
		preview["sendAs"] = in.User
		plan := writePlan{
			Summary:   fmt.Sprintf("send mail as %s to %s", in.User, strings.Join(in.To, ", ")),
			Gate:      gateSends,
			Method:    "POST",
			Base:      gapi.BaseGmail,
			Path:      "/users/" + url.PathEscape(in.User) + "/messages/send",
			Body:      preview,
			ApplyBody: map[string]any{"raw": rawMessage(mimeText)},
		}
		return appApply(ctx, gc, allowWrites, allowSends, in.User, plan)
	})
}

// --- app_list_events ---

type appListEventsInput struct {
	User       string `json:"user" jsonschema:"the target user's email address whose calendar to list (required)"`
	TimeMin    string `json:"timeMin,omitempty" jsonschema:"window start, RFC3339 (default now)"`
	TimeMax    string `json:"timeMax,omitempty" jsonschema:"window end, RFC3339 (default 30 days after timeMin)"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

func registerAppListEvents(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_list_events",
		Title:       "App: list a user's events",
		Description: "List events on an explicit user's primary calendar via the application service account, expanded to single instances and ordered by start time (default next 30 days).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appListEventsInput) (*mcp.CallToolResult, listEventsOutput, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, listEventsOutput{}, fmt.Errorf("user is required")
		}
		out, err := listPrimaryEvents(asUser(ctx, in.User), gc, in.TimeMin, in.TimeMax, in.MaxResults, in.PageToken)
		if err != nil {
			return nil, listEventsOutput{}, err
		}
		return text(fmt.Sprintf("%d events for %s", out.Count, in.User)), out, nil
	})
}

// --- app_list_files ---

type appListFilesInput struct {
	User       string `json:"user" jsonschema:"the target user's email address whose Drive to list (required)"`
	Query      string `json:"query,omitempty" jsonschema:"optional Drive search query"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

func registerAppListFiles(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_list_files",
		Title:       "App: list a user's files",
		Description: "List an explicit user's Drive files via the application service account (recent first, or filtered by a Drive query). Returns metadata; page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appListFilesInput) (*mcp.CallToolResult, listFilesOutput, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, listFilesOutput{}, fmt.Errorf("user is required")
		}
		q := url.Values{}
		q.Set("pageSize", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("orderBy", "modifiedTime desc")
		q.Set("fields", fileListFields)
		clauses := []string{"trashed = false"}
		if s := strings.TrimSpace(in.Query); s != "" {
			clauses = append([]string{"(" + s + ")"}, clauses...)
		}
		q.Set("q", strings.Join(clauses, " and "))
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(asUser(ctx, in.User), gapi.BaseDrive, "/files", q)
		if err != nil {
			return nil, listFilesOutput{}, toolError(err)
		}
		var env struct {
			Files         []DriveFile `json:"files"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listFilesOutput{}, fmt.Errorf("decoding files: %w", err)
		}
		out := listFilesOutput{Files: env.Files, Count: len(env.Files), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d files for %s", out.Count, in.User)), out, nil
	})
}

// --- app_set_vacation (write gate) ---

type appSetVacationInput struct {
	User    string `json:"user" jsonschema:"the target user's email address (required)"`
	Enable  bool   `json:"enable" jsonschema:"true to enable, false to disable"`
	Subject string `json:"subject,omitempty" jsonschema:"auto-reply subject (when enabling)"`
	Body    string `json:"body,omitempty" jsonschema:"auto-reply body (when enabling)"`
}

func registerAppSetVacation(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_set_vacation",
		Title:       "App: set a user's vacation responder",
		Description: "Enable or disable an explicit user's Gmail vacation responder via the application service account. Reversible, so it rides the write gate. Applied changes are logged with the requesting actor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appSetVacationInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.User) == "" {
			return nil, writeOutput{}, fmt.Errorf("user is required")
		}
		body := map[string]any{"enableAutoReply": in.Enable}
		if in.Enable {
			if s := strings.TrimSpace(in.Subject); s != "" {
				body["responseSubject"] = s
			}
			if s := strings.TrimSpace(in.Body); s != "" {
				body["responseBodyPlainText"] = s
			}
		}
		verb := "disable"
		if in.Enable {
			verb = "enable"
		}
		plan := writePlan{
			Summary: fmt.Sprintf("%s vacation responder for %s", verb, in.User),
			Gate:    gateWrites,
			Method:  "PUT",
			Base:    gapi.BaseGmail,
			Path:    "/users/" + url.PathEscape(in.User) + "/settings/vacation",
			Body:    body,
		}
		return appApply(ctx, gc, allowWrites, allowSends, in.User, plan)
	})
}
