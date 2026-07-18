package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGmailReadTools installs the M1 read-only Gmail tools on the server.
// Every tool acts as the signed-in user (classic-delegated mode) against
// /users/me, so Google — not this server — remains the authority on what the
// caller may read. The main thread wires this up in newMCPServer.
func registerGmailReadTools(server *mcp.Server, gc *gapi.Client) {
	registerGetProfile(server, gc)
	registerListLabels(server, gc)
	registerListMessages(server, gc)
	registerSearchMessages(server, gc)
	registerGetMessage(server, gc)
}

// maxBodyBytes caps the decoded plain-text body get_message returns, so a large
// message can't flood model context. Anything longer is truncated with a flag.
const maxBodyBytes = 100 << 10 // 100 KiB

// fields projections keep Gmail responses (and the PII fed into model context)
// to just what each tool surfaces.
const (
	labelFields       = "labels(id,name,type,messageListVisibility,labelListVisibility)"
	messageListFields = "messages(id,threadId),nextPageToken,resultSizeEstimate"
	messageMetaFields = "id,threadId,labelIds,snippet,sizeEstimate,payload/headers"
	messageFullFields = "id,threadId,labelIds,snippet,sizeEstimate,payload"
)

// metadataHeaders is the set of headers get_message requests in metadata format
// — enough to summarize a message without pulling the whole header block.
var metadataHeaders = []string{"From", "To", "Cc", "Subject", "Date"}

// --- get_profile ---

type getProfileInput struct{}

// GmailProfile is the mailbox summary returned by get_profile; its JSON tags
// double as the decode target for the Gmail users.getProfile response.
type GmailProfile struct {
	EmailAddress  string `json:"emailAddress"`
	MessagesTotal int    `json:"messagesTotal"`
	ThreadsTotal  int    `json:"threadsTotal"`
	HistoryID     string `json:"historyId,omitempty"`
}

func registerGetProfile(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_profile",
		Annotations: readAnnotations(),
		Title:       "Get Gmail profile",
		Description: "Return the signed-in user's Gmail profile: email address and total message/thread counts. Makes a live call as the current user.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ getProfileInput) (*mcp.CallToolResult, GmailProfile, error) {
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/profile", nil)
		if err != nil {
			return nil, GmailProfile{}, toolError(err)
		}
		var p GmailProfile
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, GmailProfile{}, fmt.Errorf("decoding profile: %w", err)
		}
		return text(fmt.Sprintf("%s — %d messages, %d threads", p.EmailAddress, p.MessagesTotal, p.ThreadsTotal)), p, nil
	})
}

// --- list_labels ---

type listLabelsInput struct{}

// Label is a compact Gmail label. System labels (INBOX, SENT, …) have type
// "system"; user labels have type "user".
type Label struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Type                  string `json:"type,omitempty"`
	MessageListVisibility string `json:"messageListVisibility,omitempty"`
	LabelListVisibility   string `json:"labelListVisibility,omitempty"`
}

type listLabelsOutput struct {
	Labels []Label `json:"labels"`
	Count  int     `json:"count"`
}

func registerListLabels(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_labels",
		Annotations: readAnnotations(),
		Title:       "List Gmail labels",
		Description: "List the signed-in user's Gmail labels (system labels like INBOX/SENT/UNREAD and user-created labels), with their ids for use as labelIds filters in list_messages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listLabelsInput) (*mcp.CallToolResult, listLabelsOutput, error) {
		q := url.Values{"fields": {labelFields}}
		raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/me/labels", q)
		if err != nil {
			return nil, listLabelsOutput{}, toolError(err)
		}
		var env struct {
			Labels []Label `json:"labels"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listLabelsOutput{}, fmt.Errorf("decoding labels: %w", err)
		}
		out := listLabelsOutput{Labels: env.Labels, Count: len(env.Labels)}
		return text(fmt.Sprintf("%d labels", out.Count)), out, nil
	})
}

// --- list_messages / search_messages (shared) ---

// MessageRef is the compact message summary Gmail's list endpoint returns: only
// ids. Gmail is thread-centric, so the threadId is surfaced as a first-class
// field for follow-up gets.
type MessageRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type messageListOutput struct {
	Messages           []MessageRef `json:"messages"`
	Count              int          `json:"count"`
	ResultSizeEstimate int          `json:"resultSizeEstimate,omitempty"`
	NextPageToken      string       `json:"nextPageToken,omitempty"`
}

type listMessagesInput struct {
	Query            string   `json:"query,omitempty" jsonschema:"optional Gmail search query, same syntax as the Gmail search box (e.g. 'from:alice is:unread newer_than:7d')"`
	LabelIDs         []string `json:"labelIds,omitempty" jsonschema:"optional label ids to filter by (AND); get ids from list_labels"`
	MaxResults       int      `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken        string   `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
	IncludeSpamTrash bool     `json:"includeSpamTrash,omitempty" jsonschema:"include SPAM and TRASH messages (default false)"`
}

type searchMessagesInput struct {
	Query            string   `json:"query" jsonschema:"Gmail search query, same syntax as the Gmail search box (e.g. 'from:alice is:unread newer_than:7d')"`
	LabelIDs         []string `json:"labelIds,omitempty" jsonschema:"optional label ids to further filter by (AND)"`
	MaxResults       int      `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken        string   `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
	IncludeSpamTrash bool     `json:"includeSpamTrash,omitempty" jsonschema:"include SPAM and TRASH messages (default false)"`
}

func registerListMessages(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_messages",
		Annotations: readAnnotations(),
		Title:       "List Gmail messages",
		Description: "List messages in the signed-in mailbox, most recent first. Optionally filter by a Gmail search query and/or label ids. Returns message + thread ids only (metadata is fetched per-message via get_message); page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMessagesInput) (*mcp.CallToolResult, messageListOutput, error) {
		out, err := listMessages(ctx, gc, "me", in.Query, in.LabelIDs, in.MaxResults, in.PageToken, in.IncludeSpamTrash)
		if err != nil {
			return nil, messageListOutput{}, err
		}
		return text(fmt.Sprintf("%d messages", out.Count)), out, nil
	})
}

func registerSearchMessages(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_messages",
		Annotations: readAnnotations(),
		Title:       "Search Gmail messages",
		Description: "Search the signed-in mailbox with a Gmail query (from:, to:, subject:, is:unread, has:attachment, newer_than:, before:, label:, …). Returns message + thread ids only; page with nextPageToken and fetch details via get_message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchMessagesInput) (*mcp.CallToolResult, messageListOutput, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, messageListOutput{}, fmt.Errorf("query is required")
		}
		out, err := listMessages(ctx, gc, "me", in.Query, in.LabelIDs, in.MaxResults, in.PageToken, in.IncludeSpamTrash)
		if err != nil {
			return nil, messageListOutput{}, err
		}
		return text(fmt.Sprintf("%d messages match", out.Count)), out, nil
	})
}

// listMessages is the shared body of list_messages, search_messages, and
// app_list_messages: it calls Gmail users.messages.list for the given user ("me"
// or an explicit address) with the given filters and returns one bounded page,
// exposing nextPageToken for caller-driven continuation.
func listMessages(ctx context.Context, gc *gapi.Client, user, query string, labelIDs []string, maxResults int, pageToken string, includeSpamTrash bool) (messageListOutput, error) {
	q := url.Values{}
	q.Set("maxResults", strconv.Itoa(clampLimit(maxResults)))
	q.Set("fields", messageListFields)
	if s := strings.TrimSpace(query); s != "" {
		q.Set("q", s)
	}
	for _, id := range labelIDs {
		if id = strings.TrimSpace(id); id != "" {
			q.Add("labelIds", id)
		}
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	if includeSpamTrash {
		q.Set("includeSpamTrash", "true")
	}

	raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/"+url.PathEscape(user)+"/messages", q)
	if err != nil {
		return messageListOutput{}, toolError(err)
	}
	var env struct {
		Messages           []MessageRef `json:"messages"`
		ResultSizeEstimate int          `json:"resultSizeEstimate"`
		NextPageToken      string       `json:"nextPageToken"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return messageListOutput{}, fmt.Errorf("decoding messages: %w", err)
	}
	return messageListOutput{
		Messages:           env.Messages,
		Count:              len(env.Messages),
		ResultSizeEstimate: env.ResultSizeEstimate,
		NextPageToken:      env.NextPageToken,
	}, nil
}

// --- get_message ---

type getMessageInput struct {
	ID     string `json:"id" jsonschema:"the message id from list_messages/search_messages"`
	Format string `json:"format,omitempty" jsonschema:"'metadata' for headers + snippet (default) or 'full' to also include the decoded plain-text body"`
}

// MessageDetail is the summarized single message get_message returns. Common
// headers are lifted out of the raw header block; Body is populated only in
// 'full' format.
type MessageDetail struct {
	ID            string   `json:"id"`
	ThreadID      string   `json:"threadId"`
	LabelIDs      []string `json:"labelIds,omitempty"`
	From          string   `json:"from,omitempty"`
	To            string   `json:"to,omitempty"`
	Cc            string   `json:"cc,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Date          string   `json:"date,omitempty"`
	Snippet       string   `json:"snippet,omitempty"`
	SizeEstimate  int      `json:"sizeEstimate,omitempty"`
	Body          string   `json:"body,omitempty"`
	BodyTruncated bool     `json:"bodyTruncated,omitempty"`
}

// gmailPart mirrors the recursive MIME payload tree Gmail returns.
type gmailPart struct {
	MimeType string        `json:"mimeType"`
	Headers  []gmailHeader `json:"headers"`
	Body     gmailBody     `json:"body"`
	Parts    []gmailPart   `json:"parts"`
}

type gmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailBody struct {
	Data string `json:"data"`
	Size int    `json:"size"`
}

func registerGetMessage(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_message",
		Annotations: readAnnotations(),
		Title:       "Get Gmail message",
		Description: "Fetch a single message by id. Default 'metadata' format returns the common headers (From/To/Cc/Subject/Date) plus the snippet; 'full' also returns the decoded plain-text body (capped at 100 KiB). Use ids from list_messages/search_messages.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getMessageInput) (*mcp.CallToolResult, MessageDetail, error) {
		detail, err := fetchMessageDetail(ctx, gc, "me", in.ID, in.Format)
		if err != nil {
			return nil, MessageDetail{}, err
		}
		summary := detail.Subject
		if summary == "" {
			summary = detail.Snippet
		}
		return text(fmt.Sprintf("%s — %s", detail.From, summary)), detail, nil
	})
}

// fetchMessageDetail fetches and summarizes one Gmail message for the given user
// ("me" for the signed-in user, or an explicit address for the application
// tier), validating the format. It is shared by get_message and app_get_message.
func fetchMessageDetail(ctx context.Context, gc *gapi.Client, user, id, format string) (MessageDetail, error) {
	if strings.TrimSpace(id) == "" {
		return MessageDetail{}, fmt.Errorf("id is required")
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "metadata"
	}
	if format != "metadata" && format != "full" {
		return MessageDetail{}, fmt.Errorf("format must be 'metadata' or 'full', got %q", format)
	}

	q := url.Values{}
	q.Set("format", format)
	if format == "metadata" {
		q.Set("fields", messageMetaFields)
		for _, h := range metadataHeaders {
			q.Add("metadataHeaders", h)
		}
	} else {
		q.Set("fields", messageFullFields)
	}

	raw, err := gc.Get(ctx, gapi.BaseGmail, "/users/"+url.PathEscape(user)+"/messages/"+url.PathEscape(id), q)
	if err != nil {
		return MessageDetail{}, toolError(err)
	}

	var msg struct {
		ID           string    `json:"id"`
		ThreadID     string    `json:"threadId"`
		LabelIDs     []string  `json:"labelIds"`
		Snippet      string    `json:"snippet"`
		SizeEstimate int       `json:"sizeEstimate"`
		Payload      gmailPart `json:"payload"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return MessageDetail{}, fmt.Errorf("decoding message: %w", err)
	}

	detail := MessageDetail{
		ID:           msg.ID,
		ThreadID:     msg.ThreadID,
		LabelIDs:     msg.LabelIDs,
		Snippet:      msg.Snippet,
		SizeEstimate: msg.SizeEstimate,
		From:         headerValue(msg.Payload.Headers, "From"),
		To:           headerValue(msg.Payload.Headers, "To"),
		Cc:           headerValue(msg.Payload.Headers, "Cc"),
		Subject:      headerValue(msg.Payload.Headers, "Subject"),
		Date:         headerValue(msg.Payload.Headers, "Date"),
	}
	if format == "full" {
		body, truncated := plainTextBody(msg.Payload)
		detail.Body = body
		detail.BodyTruncated = truncated
	}
	return detail, nil
}

// headerValue returns the first header whose name matches (case-insensitively),
// or "" when absent.
func headerValue(headers []gmailHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// plainTextBody walks the MIME tree for the first text/plain part, decodes its
// base64url body, and returns it capped at maxBodyBytes. The bool reports
// whether the body was truncated.
func plainTextBody(part gmailPart) (string, bool) {
	data := firstTextPlain(part)
	if data == "" {
		return "", false
	}
	decoded, err := decodeSegment(data)
	if err != nil {
		return "", false
	}
	capped, truncated := truncateUTF8(decoded, maxBodyBytes)
	return string(capped), truncated
}

// firstTextPlain returns the raw base64url body data of the first text/plain
// part found in a depth-first walk, or "" when there is none.
func firstTextPlain(part gmailPart) string {
	if strings.EqualFold(part.MimeType, "text/plain") && part.Body.Data != "" {
		return part.Body.Data
	}
	for _, p := range part.Parts {
		if data := firstTextPlain(p); data != "" {
			return data
		}
	}
	return ""
}

// decodeSegment decodes Gmail's web-safe (URL-safe) base64 body data, tolerating
// the presence or absence of padding.
func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
