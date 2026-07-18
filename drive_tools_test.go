package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type driveCapture struct {
	mu      sync.Mutex
	listQ   string
	orderBy string
	corpora string
}

func mockDrive(t *testing.T) (*httptest.Server, *driveCapture) {
	t.Helper()
	cap := &driveCapture{}
	mux := http.NewServeMux()

	// Metadata get (fields=id,name,mimeType,size) and direct download (alt=media)
	// share the /files/{id} path; branch on alt.
	mux.HandleFunc("GET /drive/v3/files/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if r.URL.Query().Get("alt") == "media" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("plain file contents"))
			return
		}
		switch id {
		case "doc1":
			writeJSON(w, http.StatusOK, `{"id":"doc1","name":"Design Notes","mimeType":"application/vnd.google-apps.document"}`)
		case "txt1":
			writeJSON(w, http.StatusOK, `{"id":"txt1","name":"notes.txt","mimeType":"text/plain","size":"19"}`)
		case "form1":
			writeJSON(w, http.StatusOK, `{"id":"form1","name":"Survey","mimeType":"application/vnd.google-apps.form"}`)
		case "pdf1":
			writeJSON(w, http.StatusOK, `{"id":"pdf1","name":"contract.pdf","mimeType":"application/pdf","size":"204800"}`)
		case "png1":
			writeJSON(w, http.StatusOK, `{"id":"png1","name":"logo.png","mimeType":"image/png","size":"51200"}`)
		case "json1":
			writeJSON(w, http.StatusOK, `{"id":"json1","name":"config.json","mimeType":"application/json","size":"12"}`)
		case "missing":
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"File not found: missing.","status":"NOT_FOUND"}}`)
		default:
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"unknown","status":"NOT_FOUND"}}`)
		}
	})

	mux.HandleFunc("GET /drive/v3/files/{id}/export", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("exported google doc body"))
	})

	mux.HandleFunc("GET /drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		cap.mu.Lock()
		q := r.URL.Query()
		cap.listQ = q.Get("q")
		cap.orderBy = q.Get("orderBy")
		cap.corpora = q.Get("corpora")
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"files":[
			{"id":"doc1","name":"Design Notes","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-07-10T12:00:00Z","owners":[{"emailAddress":"ada@example.com"}],"shared":true},
			{"id":"txt1","name":"notes.txt","mimeType":"text/plain","size":"19"}
		],"nextPageToken":"drvNext"}`)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func connectDrive(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, registerDriveReadTools)
}

func TestListFilesDefaultRecent(t *testing.T) {
	srv, cap := mockDrive(t)
	cs := connectDrive(t, srv)

	_, out := callTool(t, cs, "list_files", map[string]any{})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	if out["nextPageToken"] != "drvNext" {
		t.Errorf("nextPageToken = %v", out["nextPageToken"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.orderBy != "modifiedTime desc" {
		t.Errorf("orderBy = %q, want 'modifiedTime desc'", cap.orderBy)
	}
	// Default listing excludes trash.
	if cap.listQ != "trashed = false" {
		t.Errorf("q = %q, want 'trashed = false'", cap.listQ)
	}
}

func TestListFilesWithQueryAndSharedDrives(t *testing.T) {
	srv, cap := mockDrive(t)
	cs := connectDrive(t, srv)

	callTool(t, cs, "list_files", map[string]any{
		"query":               "name contains 'report'",
		"includeSharedDrives": true,
	})
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.listQ, "name contains 'report'") || !strings.Contains(cap.listQ, "trashed = false") {
		t.Errorf("q = %q, want caller query AND trashed filter", cap.listQ)
	}
	if cap.corpora != "allDrives" {
		t.Errorf("corpora = %q, want allDrives", cap.corpora)
	}
}

func TestGetFileContentExportsGoogleDoc(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	_, out := callTool(t, cs, "get_file_content", map[string]any{"fileId": "doc1"})
	if out["exported"] != true {
		t.Errorf("exported = %v, want true", out["exported"])
	}
	if out["content"] != "exported google doc body" {
		t.Errorf("content = %v", out["content"])
	}
}

func TestGetFileContentDownloadsPlainFile(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	_, out := callTool(t, cs, "get_file_content", map[string]any{"fileId": "txt1"})
	if out["exported"] != false {
		t.Errorf("exported = %v, want false", out["exported"])
	}
	if out["content"] != "plain file contents" {
		t.Errorf("content = %v", out["content"])
	}
}

func TestGetFileContentRejectsNoTextExport(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	msg := callToolErr(t, cs, "get_file_content", map[string]any{"fileId": "form1"})
	if !strings.Contains(msg, "no text export") {
		t.Errorf("error = %q, want 'no text export'", msg)
	}
}

// TestGetFileContentRejectsBinary is the regression test for the tool that
// promised "binary files without a text form are rejected" but only ever
// rejected Google-native types: a PDF or PNG was downloaded and handed back as a
// string of replacement characters, burning the caller's context to say nothing.
func TestGetFileContentRejectsBinary(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	for _, tc := range []struct{ id, mime string }{
		{"pdf1", "application/pdf"},
		{"png1", "image/png"},
	} {
		msg := callToolErr(t, cs, "get_file_content", map[string]any{"fileId": tc.id})
		if !strings.Contains(msg, "text only") || !strings.Contains(msg, tc.mime) {
			t.Errorf("%s: error = %q, want it to name %s and say text only", tc.id, msg, tc.mime)
		}
	}
}

// A non-text/* type that is still textual must NOT be caught by the binary
// rejection.
func TestGetFileContentAllowsTextualNonTextTypes(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	_, out := callTool(t, cs, "get_file_content", map[string]any{"fileId": "json1"})
	if out["content"] == "" {
		t.Errorf("application/json was rejected or returned empty: %v", out)
	}
}

func TestTextualMIME(t *testing.T) {
	textual := []string{
		"text/plain", "text/csv", "TEXT/HTML", "text/markdown",
		"text/plain; charset=utf-8", " application/json ",
		"application/xml", "application/ld+json", "application/atom+xml",
		"application/x-yaml", "application/javascript", "application/rtf",
	}
	for _, m := range textual {
		if !textualMIME(m) {
			t.Errorf("textualMIME(%q) = false, want true", m)
		}
	}
	binary := []string{
		"application/pdf", "image/png", "image/jpeg", "application/zip",
		"application/octet-stream", "video/mp4", "font/woff2",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"",
	}
	for _, m := range binary {
		if textualMIME(m) {
			t.Errorf("textualMIME(%q) = true, want false", m)
		}
	}
}

// truncateUTF8 must never leave a partial rune at the cut, which would render as
// a stray replacement character at the end of every truncated body.
func TestTruncateUTF8DoesNotSplitRunes(t *testing.T) {
	// "héllo wörld" — the multi-byte runes sit at byte offsets 1 and 7.
	s := []byte("héllo wörld")
	for limit := 0; limit <= len(s); limit++ {
		got, truncated := truncateUTF8(s, limit)
		if !utf8.Valid(got) {
			t.Errorf("limit %d produced invalid UTF-8: %q", limit, got)
		}
		if len(got) > limit {
			t.Errorf("limit %d returned %d bytes", limit, len(got))
		}
		if truncated != (limit < len(s)) {
			t.Errorf("limit %d: truncated = %v", limit, truncated)
		}
	}
	// A string that fits is returned whole and unflagged.
	if got, truncated := truncateUTF8(s, len(s)+10); truncated || string(got) != string(s) {
		t.Errorf("no-op truncation altered the value: %q, %v", got, truncated)
	}
}

func TestGetFileContentNotFound(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	msg := callToolErr(t, cs, "get_file_content", map[string]any{"fileId": "missing"})
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "File not found") {
		t.Errorf("error = %q, want not-found", msg)
	}
}
