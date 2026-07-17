package main

import (
	"encoding/base64"
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

	_, out := callTool(t, cs, "gmail_modify", map[string]any{
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

	msg := callToolErr(t, cs, "gmail_modify", map[string]any{"id": "m1"})
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
