package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// writeToolCapture records the last mutation the mock received.
type writeToolCapture struct {
	mu     sync.Mutex
	called bool
	path   string
	body   string
}

// mockGmailWrite serves any Gmail mutation path, recording the request.
func mockGmailWrite(t *testing.T) (*httptest.Server, *writeToolCapture) {
	t.Helper()
	cap := &writeToolCapture{}
	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.path = r.URL.Path
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"result-id","labelIds":["INBOX"]}`)
	}
	mux.HandleFunc("POST /gmail/v1/users/me/drafts", handler)
	mux.HandleFunc("POST /gmail/v1/users/me/messages/send", handler)
	mux.HandleFunc("POST /gmail/v1/users/me/messages/{id}/modify", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

// connectGmailWrite wires the Gmail write tools with the given gates.
func connectGmailWrite(t *testing.T, srv *httptest.Server, allowWrites, allowSends bool) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, func(s *mcp.Server, gc *gapi.Client) {
		registerGmailWriteTools(s, gc, allowWrites, allowSends)
	})
}

func TestGmailCreateDraftDryRun(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, false, false) // write gate closed

	_, out := callTool(t, cs, "gmail_create_draft", map[string]any{
		"to":      []any{"ada@example.com"},
		"subject": "Hi",
		"body":    "hello there",
	})
	if out["dryRun"] != true || out["applied"] == true {
		t.Errorf("expected dry-run, got %v", out)
	}
	if cap.called {
		t.Error("dry-run must not call Google")
	}
	// The readable preview shows the body, not a base64 blob.
	body := out["body"].(map[string]any)
	if body["body"] != "hello there" {
		t.Errorf("preview body = %v, want readable content", body)
	}
}

func TestGmailCreateDraftAppliesWhenWriteGateOpen(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, true, false) // write gate open, send gate closed

	_, out := callTool(t, cs, "gmail_create_draft", map[string]any{
		"to":      []any{"ada@example.com"},
		"subject": "Hi",
		"body":    "hello there",
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !cap.called || cap.path != "/gmail/v1/users/me/drafts" {
		t.Errorf("recorded call = %v %q", cap.called, cap.path)
	}
	// The wire form carries a base64url raw MIME message with the recipient.
	if !strings.Contains(cap.body, `"raw"`) {
		t.Errorf("wire body = %q, want a raw MIME message", cap.body)
	}
}

func TestGmailSendRequiresSendGateNotWriteGate(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	// Write gate OPEN but send gate CLOSED — send must still be a dry-run.
	cs := connectGmailWrite(t, srv, true, false)

	_, out := callTool(t, cs, "gmail_send", map[string]any{
		"to":      []any{"ada@example.com"},
		"subject": "Hi",
		"body":    "hello",
	})
	if out["dryRun"] != true {
		t.Errorf("gmail_send must be a dry-run when only the write gate is open: %v", out)
	}
	if cap.called {
		t.Error("gmail_send must not call Google when the send gate is closed")
	}
}

func TestGmailSendAppliesAndEncodesMIME(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, false, true) // only the send gate open

	_, out := callTool(t, cs, "gmail_send", map[string]any{
		"to":      []any{"ada@example.com"},
		"cc":      []any{"grace@example.com"},
		"subject": "Meeting",
		"body":    "See you at noon",
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	body := cap.body
	cap.mu.Unlock()

	// Decode the raw MIME and confirm headers + body round-tripped.
	raw := extractRawField(t, body)
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("raw not base64url: %v", err)
	}
	mimeStr := string(decoded)
	for _, want := range []string{"To: ada@example.com", "Cc: grace@example.com", "See you at noon"} {
		if !strings.Contains(mimeStr, want) {
			t.Errorf("MIME missing %q; got:\n%s", want, mimeStr)
		}
	}
}

func TestGmailModifyRidesWriteGate(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, true, false)

	_, out := callTool(t, cs, "gmail_modify_labels", map[string]any{
		"id":             "m1",
		"removeLabelIds": []any{"UNREAD"},
	})
	if out["applied"] != true {
		t.Errorf("expected applied, got %v", out)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.path != "/gmail/v1/users/me/messages/m1/modify" {
		t.Errorf("path = %q", cap.path)
	}
	if !strings.Contains(cap.body, "removeLabelIds") || !strings.Contains(cap.body, "UNREAD") {
		t.Errorf("body = %q", cap.body)
	}
}

func TestGmailModifyRequiresLabelChange(t *testing.T) {
	srv, _ := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, true, false)

	msg := callToolErr(t, cs, "gmail_modify_labels", map[string]any{"id": "m1"})
	if !strings.Contains(msg, "addLabelIds") {
		t.Errorf("error = %q, want a label-change requirement", msg)
	}
}

// extractRawField pulls the value of the JSON "raw" field from a request body.
func extractRawField(t *testing.T, body string) string {
	t.Helper()
	const key = `"raw":"`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatalf("no raw field in %q", body)
	}
	rest := body[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("unterminated raw field in %q", body)
	}
	return rest[:j]
}

// TestBuildMIMERejectsHeaderInjection is the regression guard for MIME header
// injection: a recipient or In-Reply-To value carrying a CR/LF must be rejected
// rather than smuggle an extra header (e.g. a hidden Bcc) into the message.
func TestBuildMIMERejectsHeaderInjection(t *testing.T) {
	injections := []struct {
		name    string
		to, cc  []string
		inReply string
	}{
		{"to newline", []string{"ok@example.com", "evil@example.com\r\nBcc: victim@example.com"}, nil, ""},
		{"cc newline", []string{"ok@example.com"}, []string{"x@example.com\nBcc: victim@example.com"}, ""},
		{"in-reply-to newline", []string{"ok@example.com"}, nil, "<id>\r\nBcc: victim@example.com"},
	}
	for _, tc := range injections {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildMIME(tc.to, tc.cc, "Subj", "body", tc.inReply); err == nil {
				t.Error("expected header-injection rejection, got nil error")
			}
		})
	}

	// A clean message — including a newline in the BODY, which is legitimate —
	// still builds.
	if _, err := buildMIME([]string{"ok@example.com"}, []string{"cc@example.com"}, "Subject line", "line one\nline two", ""); err != nil {
		t.Fatalf("clean message rejected: %v", err)
	}
	// A CR/LF in the subject is neutralized by Q-encoding, so it is not an
	// injection and must not be rejected.
	if _, err := buildMIME([]string{"ok@example.com"}, nil, "Subj\r\nBcc: x@example.com", "body", ""); err != nil {
		t.Fatalf("subject with CRLF should be Q-encoded, not rejected: %v", err)
	}
}

// gmail_reply had no behavioral test, so its threading was never exercised: the
// caller supplies threadId and the original Message-ID, and this asserts both
// reach the wire as Gmail needs them. A reply that loses them is delivered as a
// new conversation rather than in the thread.
func TestGmailReplyThreadsCorrectly(t *testing.T) {
	srv, cap := mockGmailWrite(t)

	args := map[string]any{
		"threadId":  "thread-9",
		"to":        []any{"grace@example.com"},
		"subject":   "Re: Quarterly report",
		"body":      "Thanks, looks good.",
		"inReplyTo": "<orig-42@mail.example.com>",
	}

	// Replying emails someone, so the write gate alone must not send it.
	cs := connectGmailWrite(t, srv, true, false)
	_, out := callTool(t, cs, "gmail_reply", args)
	if out["dryRun"] != true {
		t.Errorf("reply must be a dry run when only the write gate is open: %v", out)
	}
	if cap.called {
		t.Error("dry run sent the reply")
	}

	// Send gate open → applied, threaded.
	cs2 := connectGmailWrite(t, srv, false, true)
	_, out2 := callTool(t, cs2, "gmail_reply", args)
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	var sent struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal([]byte(cap.body), &sent); err != nil {
		t.Fatalf("decoding send body %q: %v", cap.body, err)
	}
	if sent.ThreadID != "thread-9" {
		t.Errorf("threadId = %q, want thread-9 — without it Gmail starts a new conversation", sent.ThreadID)
	}
	mimeBytes, err := base64.URLEncoding.DecodeString(sent.Raw)
	if err != nil {
		t.Fatalf("raw is not base64url: %v", err)
	}
	mime := string(mimeBytes)
	for _, want := range []string{
		"In-Reply-To: <orig-42@mail.example.com>",
		"References: <orig-42@mail.example.com>",
		"To: grace@example.com",
		"Thanks, looks good.",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("reply MIME missing %q:\n%s", want, mime)
		}
	}
}

// The header-injection guard must cover the reply path too, including the
// threading id, which is written into the MIME verbatim.
func TestGmailReplyRejectsHeaderInjection(t *testing.T) {
	srv, cap := mockGmailWrite(t)
	cs := connectGmailWrite(t, srv, false, true)

	msg := callToolErr(t, cs, "gmail_reply", map[string]any{
		"threadId":  "thread-9",
		"to":        []any{"grace@example.com"},
		"subject":   "Re: report",
		"body":      "ok",
		"inReplyTo": "<a@b>\r\nBcc: attacker@evil.example",
	})
	if !strings.Contains(msg, "newline") {
		t.Errorf("error = %q, want a header-injection rejection", msg)
	}
	if cap.called {
		t.Error("a header-injecting reply was sent")
	}
}
