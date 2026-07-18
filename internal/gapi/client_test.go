package gapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// staticToken is a trivial TokenSource returning a fixed value (or an error).
type staticToken struct {
	token string
	err   error
}

func (s staticToken) GoogleToken(_ context.Context) (string, error) {
	return s.token, s.err
}

// newClient points a Client at a test server via WithBaseURL and its client.
func newClient(t *testing.T, srv *httptest.Server, ts TokenSource) *Client {
	t.Helper()
	return New(ts, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestGetSendsBearerAndPreservesPath(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"emailAddress":"ada@example.com","messagesTotal":42}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "secret-token"})
	raw, err := c.Get(context.Background(), BaseGmail, "/users/me/profile", url.Values{"fields": {"emailAddress"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want bearer", gotAuth)
	}
	// The host is rewritten to the test server, but the Gmail path prefix and
	// resource path are preserved so a mux can route by path.
	if gotPath != "/gmail/v1/users/me/profile" {
		t.Errorf("path = %q, want /gmail/v1/users/me/profile", gotPath)
	}
	if gotQuery != "fields=emailAddress" {
		t.Errorf("query = %q, want fields=emailAddress", gotQuery)
	}
	if !strings.Contains(string(raw), "ada@example.com") {
		t.Errorf("body = %s", raw)
	}
}

func TestGetTokenErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be reached when the token source fails")
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{err: errors.New("sign-in required")})
	if _, err := c.Get(context.Background(), BaseGmail, "/users/me/profile", nil); err == nil {
		t.Fatal("expected token error, got nil")
	}
}

func TestGetDecodesGoogleError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not Found","status":"NOT_FOUND"}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	_, err := c.Get(context.Background(), BaseGmail, "/users/me/messages/nope", nil)
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error = %v, want *gapi.Error", err)
	}
	if ge.Status != 404 || ge.Reason != "NOT_FOUND" || ge.Message != "Not Found" {
		t.Errorf("decoded error = %+v", ge)
	}
}

func TestGetDecodesLegacyErrorReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Insufficient Permission","errors":[{"reason":"insufficientPermissions"}]}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	_, err := c.Get(context.Background(), BaseGmail, "/users/me/labels", nil)
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error = %v, want *gapi.Error", err)
	}
	if ge.Reason != "insufficientPermissions" {
		t.Errorf("reason = %q, want insufficientPermissions (from errors[].reason)", ge.Reason)
	}
}

func TestRetryOn429ThenSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Retry-After", "0") // immediate retry, keeps the test fast
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	if _, err := c.Get(context.Background(), BaseGmail, "/users/me/profile", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (one 429 then success)", attempts)
	}
}

func TestRetryOnRateLimit403(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"rate","errors":[{"reason":"userRateLimitExceeded"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	if _, err := c.Get(context.Background(), BaseGmail, "/users/me/profile", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (rate-limit 403 is retried)", attempts)
	}
}

func TestPostSendsJSONBody(t *testing.T) {
	var gotMethod, gotBody, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"calendars":{"primary":{"busy":[]}}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	raw, err := c.Post(context.Background(), BaseCalendar, "/freeBusy", nil, map[string]any{"timeMin": "2026-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if !strings.Contains(gotBody, `"timeMin":"2026-01-01T00:00:00Z"`) {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(string(raw), "busy") {
		t.Errorf("response = %s", raw)
	}
}

func TestPatchSendsBodyAndReturnsResource(t *testing.T) {
	var gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"ev1","summary":"Updated"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	raw, err := c.Patch(context.Background(), BaseCalendar, "/calendars/primary/events/ev1", nil, map[string]any{"summary": "Updated"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", gotMethod)
	}
	if !strings.Contains(gotBody, `"summary":"Updated"`) {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(string(raw), "Updated") {
		t.Errorf("response = %s", raw)
	}
}

func TestDeletePassesQuery(t *testing.T) {
	var gotMethod, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	_, err := c.Delete(context.Background(), BaseCalendar, "/calendars/primary/events/ev1", url.Values{"sendUpdates": {"all"}})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotQuery != "sendUpdates=all" {
		t.Errorf("query = %q, want sendUpdates=all", gotQuery)
	}
}

func TestPostRawSendsContentType(t *testing.T) {
	var gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"file1","name":"upload.txt"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	raw, err := c.PostRaw(context.Background(), BaseDriveUpload, "/files", url.Values{"uploadType": {"multipart"}}, "multipart/related; boundary=abc", []byte("--abc--"))
	if err != nil {
		t.Fatalf("PostRaw: %v", err)
	}
	if gotContentType != "multipart/related; boundary=abc" {
		t.Errorf("content-type = %q", gotContentType)
	}
	if string(gotBody) != "--abc--" {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(string(raw), "file1") {
		t.Errorf("response = %s", raw)
	}
}

func TestPostDecodesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Bad Request","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	_, err := c.Post(context.Background(), BaseCalendar, "/freeBusy", nil, map[string]any{})
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != 400 || ge.Reason != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want decoded 400 INVALID_ARGUMENT", err)
	}
}

func TestGetRawReturnsBytesAndContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("exported document text"))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	body, ct, err := c.GetRaw(context.Background(), BaseDrive, "/files/abc/export", url.Values{"mimeType": {"text/plain"}})
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(body) != "exported document text" {
		t.Errorf("body = %q", body)
	}
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain", ct)
	}
}

func TestGetRawDecodesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"The user does not have sufficient permissions.","status":"PERMISSION_DENIED"}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	_, _, err := c.GetRaw(context.Background(), BaseDrive, "/files/abc", url.Values{"alt": {"media"}})
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != 403 {
		t.Fatalf("error = %v, want decoded 403", err)
	}
}

func TestNoRetryOnAuthz403(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":403,"message":"denied","errors":[{"reason":"insufficientPermissions"}]}}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, staticToken{token: "t"})
	if _, err := c.Get(context.Background(), BaseGmail, "/users/me/labels", nil); err == nil {
		t.Fatal("expected error")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a genuine authz 403 must not be retried)", attempts)
	}
}
