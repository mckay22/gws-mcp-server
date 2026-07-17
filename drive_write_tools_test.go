package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type driveWriteCapture struct {
	mu          sync.Mutex
	called      bool
	path        string
	contentType string
	sendNotify  string
	body        string
}

func mockDriveWrite(t *testing.T) (*httptest.Server, *driveWriteCapture) {
	t.Helper()
	cap := &driveWriteCapture{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upload/drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.path = r.URL.Path
		cap.contentType = r.Header.Get("Content-Type")
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"file-new","name":"notes.txt"}`)
	})
	mux.HandleFunc("POST /drive/v3/files/{id}/permissions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.path = r.URL.Path
		cap.sendNotify = r.URL.Query().Get("sendNotificationEmail")
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"perm-new","role":"reader","type":"user"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectDriveWrite(t *testing.T, srv *httptest.Server, allowWrites, allowSends bool) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, func(s *mcp.Server, gc *gapi.Client) {
		registerDriveWriteTools(s, gc, allowWrites, allowSends)
	})
}

func TestUploadFileRidesWriteGate(t *testing.T) {
	srv, cap := mockDriveWrite(t)

	// Gate closed → dry-run, no call, readable preview.
	cs := connectDriveWrite(t, srv, false, false)
	_, out := callTool(t, cs, "upload_file", map[string]any{
		"name":    "notes.txt",
		"content": "hello file",
	})
	if out["dryRun"] != true {
		t.Errorf("expected dry-run, got %v", out)
	}
	if cap.called {
		t.Error("dry-run must not upload")
	}

	// Gate open → multipart upload applied.
	cs2 := connectDriveWrite(t, srv, true, false)
	_, out2 := callTool(t, cs2, "upload_file", map[string]any{
		"name":    "notes.txt",
		"content": "hello file",
	})
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.HasPrefix(cap.contentType, "multipart/related") {
		t.Errorf("content-type = %q, want multipart/related", cap.contentType)
	}
	if !strings.Contains(cap.body, `"name":"notes.txt"`) || !strings.Contains(cap.body, "hello file") {
		t.Errorf("multipart body missing metadata or content: %q", cap.body)
	}
}

func TestShareFileRidesSendGate(t *testing.T) {
	srv, cap := mockDriveWrite(t)

	// Write gate open, send gate closed → dry-run (sharing is egress).
	cs := connectDriveWrite(t, srv, true, false)
	_, out := callTool(t, cs, "share_file", map[string]any{
		"fileId":       "file1",
		"role":         "reader",
		"type":         "user",
		"emailAddress": "grace@example.com",
	})
	if out["dryRun"] != true {
		t.Errorf("share_file must be a dry-run when only the write gate is open: %v", out)
	}
	if cap.called {
		t.Error("must not share when the send gate is closed")
	}

	// Send gate open → applied.
	cs2 := connectDriveWrite(t, srv, false, true)
	_, out2 := callTool(t, cs2, "share_file", map[string]any{
		"fileId":       "file1",
		"role":         "reader",
		"type":         "user",
		"emailAddress": "grace@example.com",
	})
	if out2["applied"] != true {
		t.Errorf("expected applied, got %v", out2)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.path != "/drive/v3/files/file1/permissions" {
		t.Errorf("path = %q", cap.path)
	}
	if !strings.Contains(cap.body, "grace@example.com") || !strings.Contains(cap.body, `"role":"reader"`) {
		t.Errorf("body = %q", cap.body)
	}
}

func TestShareFileValidatesGrantee(t *testing.T) {
	srv, _ := mockDriveWrite(t)
	cs := connectDriveWrite(t, srv, false, true)

	// type user without emailAddress → error.
	msg := callToolErr(t, cs, "share_file", map[string]any{
		"fileId": "file1",
		"role":   "reader",
		"type":   "user",
	})
	if !strings.Contains(msg, "emailAddress is required") {
		t.Errorf("error = %q", msg)
	}
}

func TestShareFileValidatesRole(t *testing.T) {
	srv, _ := mockDriveWrite(t)
	cs := connectDriveWrite(t, srv, false, true)

	msg := callToolErr(t, cs, "share_file", map[string]any{
		"fileId":       "file1",
		"role":         "owner",
		"type":         "user",
		"emailAddress": "grace@example.com",
	})
	if !strings.Contains(msg, "role must be") {
		t.Errorf("error = %q", msg)
	}
}
