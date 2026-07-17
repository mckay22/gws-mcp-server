package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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

func TestGetFileContentNotFound(t *testing.T) {
	srv, _ := mockDrive(t)
	cs := connectDrive(t, srv)

	msg := callToolErr(t, cs, "get_file_content", map[string]any{"fileId": "missing"})
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "File not found") {
		t.Errorf("error = %q, want not-found", msg)
	}
}
