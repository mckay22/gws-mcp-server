package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGmailWriteTools installs the M3 gated Gmail mutation tools. Two
// independent gates guard them: creating a draft or relabeling a message is a
// reversible write (allowWrites), while sending or replying is irreversible and
// sits behind the SEPARATE send gate (allowSends). All act on the signed-in
// user's own mailbox; Google enforces whether the caller may perform them.
func registerGmailWriteTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerGmailCreateDraft(server, gc, allowWrites, allowSends)
	registerGmailModify(server, gc, allowWrites, allowSends)
	registerGmailSend(server, gc, allowWrites, allowSends)
	registerGmailReply(server, gc, allowWrites, allowSends)
}

// headerSafe rejects any value containing a CR or LF, which — interpolated into
// a MIME header — would let a caller inject extra headers (e.g. a smuggled
// Bcc: to exfiltrate a copy). It guards the header values that are written
// verbatim: recipient addresses and the In-Reply-To id. The subject is safe
// because it is RFC 2047 Q-encoded (which neutralizes CR/LF), and the body is
// safe because it follows the header/body separator.
func headerSafe(values ...string) error {
	for _, v := range values {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("header value must not contain a newline (possible header injection): %q", v)
		}
	}
	return nil
}

// buildMIME assembles a minimal RFC 2822 message. The From is left to Gmail
// (it uses the authenticated user), so only To/Cc/Subject/body are set. inReplyTo
// and references, when set, thread the reply. A plain-text body is used. It
// rejects newline-bearing header values (see headerSafe) rather than emit a
// header-injectable message.
func buildMIME(to, cc []string, subject, body, inReplyTo string) (string, error) {
	headers := append(append([]string{}, to...), cc...)
	headers = append(headers, inReplyTo)
	if err := headerSafe(headers...); err != nil {
		return "", err
	}

	var b strings.Builder
	if len(to) > 0 {
		fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	}
	if len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	// Encode the subject per RFC 2047 so non-ASCII is transmitted safely.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: %s\r\n", inReplyTo)
		fmt.Fprintf(&b, "References: %s\r\n", inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.String(), nil
}

// rawMessage base64url-encodes a MIME message for Gmail's {raw} wire form.
func rawMessage(mimeText string) string {
	return base64.URLEncoding.EncodeToString([]byte(mimeText))
}

// readablePreview is the human-facing display of an outgoing message, shown in
// both the dry-run and applied output instead of the base64url wire form.
func readablePreview(to, cc []string, subject, body string) map[string]any {
	m := map[string]any{"to": to, "subject": subject, "body": body}
	if len(cc) > 0 {
		m["cc"] = cc
	}
	return m
}

// --- gmail_create_draft (write gate) ---

type gmailCreateDraftInput struct {
	To      []string `json:"to" jsonschema:"recipient email addresses (required)"`
	Cc      []string `json:"cc,omitempty" jsonschema:"carbon-copy email addresses"`
	Subject string   `json:"subject" jsonschema:"the message subject (required)"`
	Body    string   `json:"body" jsonschema:"the plain-text message body (required)"`
}

func registerGmailCreateDraft(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_create_draft",
		Title:       "Create Gmail draft",
		Description: "Create a draft email in the signed-in user's mailbox (POST /users/me/drafts). A draft is saved but NOT sent, so it rides the ordinary write gate — without " + config.EnvAllowWrites + "=true (or --allow-writes) it returns a dry-run preview instead of calling Google.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gmailCreateDraftInput) (*mcp.CallToolResult, writeOutput, error) {
		if len(in.To) == 0 || strings.TrimSpace(in.Subject) == "" {
			return nil, writeOutput{}, fmt.Errorf("to and subject are required")
		}
		mimeText, err := buildMIME(in.To, in.Cc, in.Subject, in.Body, "")
		if err != nil {
			return nil, writeOutput{}, err
		}
		plan := writePlan{
			Summary:     fmt.Sprintf("create draft to %s: %q", strings.Join(in.To, ", "), in.Subject),
			Gate:        gateWrites,
			Method:      "POST",
			Base:        gapi.BaseGmail,
			Path:        "/users/me/drafts",
			Body:        readablePreview(in.To, in.Cc, in.Subject, in.Body),
			ApplyBody:   map[string]any{"message": map[string]any{"raw": rawMessage(mimeText)}},
			PreviewBody: readablePreview(in.To, in.Cc, in.Subject, in.Body),
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- gmail_modify (write gate) ---

type gmailModifyInput struct {
	ID             string   `json:"id" jsonschema:"the message id to modify"`
	AddLabelIDs    []string `json:"addLabelIds,omitempty" jsonschema:"label ids to add (e.g. add 'STARRED'; remove 'UNREAD' to mark read; add 'TRASH'? use dedicated trash instead)"`
	RemoveLabelIDs []string `json:"removeLabelIds,omitempty" jsonschema:"label ids to remove (e.g. remove 'INBOX' to archive, remove 'UNREAD' to mark read)"`
}

func registerGmailModify(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_modify",
		Title:       "Modify Gmail message labels",
		Description: "Add and/or remove labels on a message (POST /users/me/messages/{id}/modify) — the mechanism behind read/unread (UNREAD label), archive (remove INBOX), star, and custom labels. Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gmailModifyInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.ID) == "" {
			return nil, writeOutput{}, fmt.Errorf("id is required")
		}
		if len(in.AddLabelIDs) == 0 && len(in.RemoveLabelIDs) == 0 {
			return nil, writeOutput{}, fmt.Errorf("provide addLabelIds and/or removeLabelIds")
		}
		body := map[string]any{}
		if len(in.AddLabelIDs) > 0 {
			body["addLabelIds"] = in.AddLabelIDs
		}
		if len(in.RemoveLabelIDs) > 0 {
			body["removeLabelIds"] = in.RemoveLabelIDs
		}
		plan := writePlan{
			Summary: fmt.Sprintf("modify labels on message %s (+%v -%v)", in.ID, in.AddLabelIDs, in.RemoveLabelIDs),
			Gate:    gateWrites,
			Method:  "POST",
			Base:    gapi.BaseGmail,
			Path:    "/users/me/messages/" + in.ID + "/modify",
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- gmail_send (send gate) ---

type gmailSendInput struct {
	To      []string `json:"to" jsonschema:"recipient email addresses (required)"`
	Cc      []string `json:"cc,omitempty" jsonschema:"carbon-copy email addresses"`
	Subject string   `json:"subject" jsonschema:"the message subject (required)"`
	Body    string   `json:"body" jsonschema:"the plain-text message body (required)"`
}

func registerGmailSend(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_send",
		Title:       "Send Gmail message",
		Description: "Send an email as the signed-in user (POST /users/me/messages/send). Sending is irreversible, so it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview showing the full To/Cc/Subject/body (nothing redacted — the point is to SEE the mail before sending) instead of calling Google.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gmailSendInput) (*mcp.CallToolResult, writeOutput, error) {
		if len(in.To) == 0 || strings.TrimSpace(in.Subject) == "" {
			return nil, writeOutput{}, fmt.Errorf("to and subject are required")
		}
		mimeText, err := buildMIME(in.To, in.Cc, in.Subject, in.Body, "")
		if err != nil {
			return nil, writeOutput{}, err
		}
		plan := writePlan{
			Summary:   fmt.Sprintf("send mail to %s: %q", strings.Join(in.To, ", "), in.Subject),
			Gate:      gateSends,
			Method:    "POST",
			Base:      gapi.BaseGmail,
			Path:      "/users/me/messages/send",
			Body:      readablePreview(in.To, in.Cc, in.Subject, in.Body),
			ApplyBody: map[string]any{"raw": rawMessage(mimeText)},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- gmail_reply (send gate) ---

type gmailReplyInput struct {
	ThreadID  string   `json:"threadId" jsonschema:"the thread id to reply within (from list_messages/get_message)"`
	To        []string `json:"to" jsonschema:"recipient email addresses (required)"`
	Cc        []string `json:"cc,omitempty" jsonschema:"carbon-copy email addresses"`
	Subject   string   `json:"subject" jsonschema:"the reply subject (required; typically 'Re: …')"`
	Body      string   `json:"body" jsonschema:"the plain-text reply body (required)"`
	InReplyTo string   `json:"inReplyTo,omitempty" jsonschema:"the Message-ID header of the message being replied to, for proper threading"`
}

func registerGmailReply(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "gmail_reply",
		Title:       "Reply to Gmail thread",
		Description: "Send a reply within an existing thread as the signed-in user (POST /users/me/messages/send with threadId). Sending is irreversible, so it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview instead of calling Google.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gmailReplyInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.ThreadID) == "" || len(in.To) == 0 {
			return nil, writeOutput{}, fmt.Errorf("threadId and to are required")
		}
		mimeText, err := buildMIME(in.To, in.Cc, in.Subject, in.Body, in.InReplyTo)
		if err != nil {
			return nil, writeOutput{}, err
		}
		preview := readablePreview(in.To, in.Cc, in.Subject, in.Body)
		preview["threadId"] = in.ThreadID
		plan := writePlan{
			Summary:   fmt.Sprintf("reply in thread %s to %s", in.ThreadID, strings.Join(in.To, ", ")),
			Gate:      gateSends,
			Method:    "POST",
			Base:      gapi.BaseGmail,
			Path:      "/users/me/messages/send",
			Body:      preview,
			ApplyBody: map[string]any{"raw": rawMessage(mimeText), "threadId": in.ThreadID},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}
