package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeTS is a trivial TokenSource: the Gmail tools never inspect the token, so a
// static value is enough to exercise the real gapi client end to end.
type fakeTS struct{}

func (fakeTS) GoogleToken(_ context.Context) (string, error) { return "test-token", nil }

// bodyText is the plain-text body the mock returns for a full-format get; the
// test asserts it round-trips through base64url decoding.
const bodyText = "This is the plain-text body.\nSecond line."

// gmailCapture records what the mock saw on the last messages-list and
// message-get requests, so tests can assert query wiring reached Gmail.
type gmailCapture struct {
	mu         sync.Mutex
	q          string
	labelIDs   []string
	maxResults string
	pageToken  string
	getFormat  string
}

func (c *gmailCapture) recordList(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := r.URL.Query()
	c.q = q.Get("q")
	c.labelIDs = q["labelIds"]
	c.maxResults = q.Get("maxResults")
	c.pageToken = q.Get("pageToken")
}

func (c *gmailCapture) recordGet(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getFormat = r.URL.Query().Get("format")
}

// mockGmail stands up an httptest server returning canned, product-neutral Gmail
// JSON (example.com addresses, placeholder ids).
func mockGmail(t *testing.T) (*httptest.Server, *gmailCapture) {
	t.Helper()
	cap := &gmailCapture{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /gmail/v1/users/me/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"emailAddress":"ada@example.com","messagesTotal":1280,"threadsTotal":940,"historyId":"55555"}`)
	})

	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{"labels":[
			{"id":"INBOX","name":"INBOX","type":"system"},
			{"id":"UNREAD","name":"UNREAD","type":"system"},
			{"id":"Label_12","name":"Receipts","type":"user","labelListVisibility":"labelShow"}
		]}`)
	})

	mux.HandleFunc("GET /gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		cap.recordList(r)
		writeJSON(w, http.StatusOK, `{"messages":[
			{"id":"m1","threadId":"t1"},
			{"id":"m2","threadId":"t1"}
		],"resultSizeEstimate":2,"nextPageToken":"next123"}`)
	})

	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		cap.recordGet(r)
		if r.PathValue("id") == "missing" {
			writeJSON(w, http.StatusNotFound, `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`)
			return
		}
		b64 := base64.RawURLEncoding.EncodeToString([]byte(bodyText))
		writeJSON(w, http.StatusOK, fmt.Sprintf(`{
			"id":"m1","threadId":"t1","labelIds":["INBOX","UNREAD"],
			"snippet":"This is the plain-text body.",
			"sizeEstimate":2048,
			"payload":{
				"mimeType":"multipart/alternative",
				"headers":[
					{"name":"From","value":"Ada Lovelace <ada@example.com>"},
					{"name":"To","value":"Grace Hopper <grace@example.com>"},
					{"name":"Subject","value":"Analytical engine notes"},
					{"name":"Date","value":"Mon, 06 Jul 2026 10:00:00 +0000"}
				],
				"parts":[
					{"mimeType":"text/plain","body":{"data":%q,"size":%d}},
					{"mimeType":"text/html","body":{"data":"PGgxPmhpPC9oMT4","size":13}}
				]
			}
		}`, b64, len(bodyText)))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cap
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// connectRegistered wires a tool group (via its registrar) onto an MCP server
// backed by a real gapi client pointed at the mock, and returns a connected
// client session.
func connectRegistered(t *testing.T, srv *httptest.Server, register func(*mcp.Server, *gapi.Client)) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	gc := gapi.New(fakeTS{}, gapi.WithBaseURL(srv.URL))

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "t"}, nil)
	register(server, gc)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "t"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// connectGmail wires the Gmail read tools onto an MCP server backed by the mock.
func connectGmail(t *testing.T, srv *httptest.Server) *mcp.ClientSession {
	t.Helper()
	return connectRegistered(t, srv, registerGmailReadTools)
}

// callToolErr invokes a tool expecting a tool error, and returns its text.
func callToolErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(%s): expected a tool error, got success", name)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestGetProfile(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	_, out := callTool(t, cs, "get_profile", map[string]any{})
	if out["emailAddress"] != "ada@example.com" {
		t.Errorf("emailAddress = %v", out["emailAddress"])
	}
	if out["messagesTotal"] != float64(1280) {
		t.Errorf("messagesTotal = %v, want 1280", out["messagesTotal"])
	}
}

func TestListLabels(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	_, out := callTool(t, cs, "list_labels", map[string]any{})
	if out["count"] != float64(3) {
		t.Errorf("count = %v, want 3", out["count"])
	}
	labels, ok := out["labels"].([]any)
	if !ok || len(labels) != 3 {
		t.Fatalf("labels = %v", out["labels"])
	}
	first := labels[0].(map[string]any)
	if first["id"] != "INBOX" || first["type"] != "system" {
		t.Errorf("first label = %v", first)
	}
}

func TestListMessagesWiresQuery(t *testing.T) {
	srv, cap := mockGmail(t)
	cs := connectGmail(t, srv)

	_, out := callTool(t, cs, "list_messages", map[string]any{
		"query":      "from:alice is:unread",
		"labelIds":   []any{"INBOX", "UNREAD"},
		"maxResults": float64(10),
	})
	if out["count"] != float64(2) {
		t.Errorf("count = %v, want 2", out["count"])
	}
	if out["nextPageToken"] != "next123" {
		t.Errorf("nextPageToken = %v, want next123", out["nextPageToken"])
	}

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.q != "from:alice is:unread" {
		t.Errorf("recorded q = %q", cap.q)
	}
	if strings.Join(cap.labelIDs, ",") != "INBOX,UNREAD" {
		t.Errorf("recorded labelIds = %v", cap.labelIDs)
	}
	if cap.maxResults != "10" {
		t.Errorf("recorded maxResults = %q, want 10", cap.maxResults)
	}
}

func TestListMessagesClampsMaxResults(t *testing.T) {
	srv, cap := mockGmail(t)
	cs := connectGmail(t, srv)

	// Over the cap → clamped to maxLimit; zero would default, so test the cap.
	callTool(t, cs, "list_messages", map[string]any{"maxResults": float64(9999)})
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.maxResults != fmt.Sprint(maxLimit) {
		t.Errorf("recorded maxResults = %q, want clamp to %d", cap.maxResults, maxLimit)
	}
}

func TestSearchMessagesRequiresQuery(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	msg := callToolErr(t, cs, "search_messages", map[string]any{"query": "   "})
	if !strings.Contains(msg, "query is required") {
		t.Errorf("error = %q, want 'query is required'", msg)
	}
}

func TestGetMessageMetadata(t *testing.T) {
	srv, cap := mockGmail(t)
	cs := connectGmail(t, srv)

	_, out := callTool(t, cs, "get_message", map[string]any{"id": "m1"})
	if out["from"] != "Ada Lovelace <ada@example.com>" {
		t.Errorf("from = %v", out["from"])
	}
	if out["subject"] != "Analytical engine notes" {
		t.Errorf("subject = %v", out["subject"])
	}
	// Metadata format must not include a decoded body.
	if _, present := out["body"]; present {
		t.Errorf("metadata get should omit body, got %v", out["body"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.getFormat != "metadata" {
		t.Errorf("recorded format = %q, want metadata", cap.getFormat)
	}
}

func TestGetMessageFullDecodesBody(t *testing.T) {
	srv, cap := mockGmail(t)
	cs := connectGmail(t, srv)

	_, out := callTool(t, cs, "get_message", map[string]any{"id": "m1", "format": "full"})
	if out["body"] != bodyText {
		t.Errorf("body = %q, want %q", out["body"], bodyText)
	}
	if _, truncated := out["bodyTruncated"]; truncated {
		t.Errorf("bodyTruncated should be omitted for a small body, got %v", out["bodyTruncated"])
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.getFormat != "full" {
		t.Errorf("recorded format = %q, want full", cap.getFormat)
	}
}

func TestGetMessageValidatesFormat(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	msg := callToolErr(t, cs, "get_message", map[string]any{"id": "m1", "format": "bogus"})
	if !strings.Contains(msg, "format must be") {
		t.Errorf("error = %q, want format validation message", msg)
	}
}

func TestGetMessageNotFound(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	msg := callToolErr(t, cs, "get_message", map[string]any{"id": "missing"})
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "not be found") && !strings.Contains(msg, "Requested entity") {
		t.Errorf("error = %q, want a not-found message", msg)
	}
}

func TestGetMessageRequiresID(t *testing.T) {
	srv, _ := mockGmail(t)
	cs := connectGmail(t, srv)

	// The SDK rejects an entirely-missing id via schema validation; a present but
	// blank id is caught by the handler.
	msg := callToolErr(t, cs, "get_message", map[string]any{"id": "   "})
	if !strings.Contains(msg, "id is required") {
		t.Errorf("error = %q, want 'id is required'", msg)
	}
}
