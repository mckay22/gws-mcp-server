// Package gapi is a thin HTTP client for the Google REST APIs (Gmail, Calendar,
// Drive, the Admin SDK, Reports, …). It deliberately avoids the generated
// google.golang.org/api clients (whose model trees bloat the binary): it owns
// just the request plumbing the tools need — bearer auth, fields projection, and
// Retry-After / 429 / 503 / rate-limit-403 backoff — and returns raw JSON for
// callers to decode into their own shapes.
//
// It deliberately offers no follow-every-page helper. Each list tool fetches one
// bounded page and surfaces Google's nextPageToken, so the caller decides whether
// to continue; a client-side auto-pager would quietly turn one tool call into an
// unbounded dump into model context.
//
// Unlike a single-host client, Google's APIs live on several hosts
// (gmail.googleapis.com, www.googleapis.com, admin.googleapis.com). Callers pass
// the fully-qualified service base (see the Base* constants) plus a
// resource path; tests point every request at one httptest server via
// WithBaseURL, which rewrites the scheme+host while preserving the path so a
// single mux can route by path prefix.
//
// Tokens come from a caller-supplied TokenSource so every operating mode
// (classic-delegated personal token, resource-server DWD/linked token) reuses
// one client. The token is attached to every request and is never logged.
package gapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Service base URLs — the fully-qualified host + version prefix for each Google
// API. Callers pass one of these to Get/List alongside a resource path.
const (
	// BaseGmail is the Gmail API v1 base. Resource paths are user-scoped, e.g.
	// "/users/me/profile".
	BaseGmail = "https://gmail.googleapis.com/gmail/v1"

	// BaseCalendar is the Google Calendar API v3 base, e.g.
	// "/users/me/calendarList" or "/calendars/{id}/events".
	BaseCalendar = "https://www.googleapis.com/calendar/v3"

	// BaseDrive is the Google Drive API v3 base, e.g. "/files".
	BaseDrive = "https://www.googleapis.com/drive/v3"

	// BaseDriveUpload is Drive's separate upload host, used for file uploads
	// (uploadType=multipart/media); metadata operations use BaseDrive.
	BaseDriveUpload = "https://www.googleapis.com/upload/drive/v3"

	// BaseDirectory is the Admin SDK Directory API v1 base, e.g. "/users" or
	// "/groups/{key}/members".
	BaseDirectory = "https://admin.googleapis.com/admin/directory/v1"

	// BaseReports is the Admin SDK Reports API v1 base (audit activities), e.g.
	// "/activity/users/{key}/applications/{app}".
	BaseReports = "https://admin.googleapis.com/admin/reports/v1"

	// BaseLicensing is the Enterprise License Manager API v1 base, e.g.
	// "/product/{productId}/users".
	BaseLicensing = "https://licensing.googleapis.com/apps/licensing/v1"

	// BaseTasks is the Google Tasks API v1 base, e.g. "/users/@me/lists".
	BaseTasks = "https://tasks.googleapis.com/tasks/v1"

	// BasePeople is the People API v1 base, e.g. "/people:searchContacts".
	BasePeople = "https://people.googleapis.com/v1"

	// BaseChat is the Google Chat API v1 base (Workspace-only), e.g. "/spaces".
	BaseChat = "https://chat.googleapis.com/v1"

	// BaseMeet is the Google Meet API v2 base (edition-gated), e.g.
	// "/conferenceRecords".
	BaseMeet = "https://meet.googleapis.com/v2"
)

const (
	// defaultTimeout bounds each HTTP round-trip.
	defaultTimeout = 30 * time.Second

	// maxResponseBytes caps how much of a single response we read, so a
	// misbehaving endpoint can't exhaust memory.
	maxResponseBytes = 8 << 20 // 8 MiB

	// maxRetries is how many times a throttled/unavailable response (429, 503,
	// or a rate-limit 403) is retried before the error is surfaced.
	maxRetries = 4

	// baseRetryDelay is the first backoff step when no Retry-After is given; it
	// doubles each attempt up to maxRetryDelay.
	baseRetryDelay = 500 * time.Millisecond

	// maxRetryDelay caps any single backoff wait, so a hostile or huge
	// Retry-After can't stall the caller indefinitely.
	maxRetryDelay = 30 * time.Second
)

// TokenSource supplies a Google access token for a request. Implementations may
// cache and refresh; the client calls it once per HTTP request and puts the
// result in the Authorization header — never in a log.
type TokenSource interface {
	GoogleToken(ctx context.Context) (string, error)
}

// Option customizes a Client at construction. See WithBaseURL, WithHTTPClient.
type Option func(*Client)

// WithBaseURL rewrites the scheme+host of every request to point at base
// (preserving each request's path and query), so tests can route all Google
// hosts through one httptest server. An empty value is ignored.
func WithBaseURL(base string) Option {
	return func(c *Client) {
		if base != "" {
			c.hostOverride = strings.TrimRight(base, "/")
		}
	}
}

// WithHTTPClient overrides the underlying *http.Client (for tests, or custom
// transports/timeouts). A nil client is ignored.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

// Client talks to the Google REST APIs. Construct it with New.
type Client struct {
	// hostOverride, when set, replaces the scheme+host of every request URL
	// (tests only); the path and query are preserved.
	hostOverride string
	http         *http.Client
	ts           TokenSource
}

// New returns a Client that draws tokens from ts. Without options it targets the
// real Google API hosts over an *http.Client with a 30s timeout.
func New(ts TokenSource, opts ...Option) *Client {
	c := &Client{
		http: &http.Client{Timeout: defaultTimeout},
		ts:   ts,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Error is a decoded Google API error. Non-2xx responses carry an envelope of
// the form {"error":{"code":404,"message":"...","status":"NOT_FOUND",
// "errors":[{"reason":"..."}]}}; this captures it alongside the HTTP status.
// Callers can match it with errors.As.
type Error struct {
	Status  int    // HTTP status code
	Message string // human-readable message from the envelope
	Reason  string // Google's status/reason string, e.g. "NOT_FOUND" or "rateLimitExceeded"
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("google: %d %s: %s", e.Status, e.Reason, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("google: %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("google: %d", e.Status)
}

// Get fetches a single resource at base+path (e.g. BaseGmail,
// "/users/me/profile") and returns the raw JSON body on any 2xx status. A
// non-2xx status is decoded into an *Error. The query, if any, is appended to
// the URL.
func (c *Client) Get(ctx context.Context, base, path string, query url.Values) (json.RawMessage, error) {
	rawURL, err := c.endpoint(base, path, query)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := statusError(resp); err != nil {
		return nil, err
	}
	return json.RawMessage(resp.body), nil
}

// GetRaw fetches base+path and returns the response body verbatim (not decoded
// as JSON) along with its Content-Type, on any 2xx status. It backs media
// downloads and exports (Drive alt=media / files.export) that return file bytes
// rather than JSON. A non-2xx status is decoded into an *Error.
//
// The body is read through the shared maxResponseBytes cap and is silently
// TRUNCATED (no error, no signal) if the response exceeds it. The current caller
// (get_file_content) re-caps far below that and sets its own truncation flag, so
// this is invisible today — but any future full-fidelity download caller MUST
// account for the cap rather than assume it received the complete object.
func (c *Client) GetRaw(ctx context.Context, base, path string, query url.Values) ([]byte, string, error) {
	rawURL, err := c.endpoint(base, path, query)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	if err := statusError(resp); err != nil {
		return nil, "", err
	}
	return resp.body, resp.header.Get("Content-Type"), nil
}

// Post issues a POST to base+path with a JSON-encoded body and returns the raw
// response body on any 2xx status. A nil body sends no request body. It serves
// both read-shaped POSTs (e.g. Calendar freeBusy queries) and gated mutations. A
// non-2xx status is decoded into an *Error. The request body is never logged.
func (c *Client) Post(ctx context.Context, base, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.writeJSON(ctx, http.MethodPost, base, path, query, body)
}

// Patch issues a PATCH to base+path with a JSON-encoded body and returns the raw
// response body on any 2xx status. Google's PATCH typically returns the updated
// resource. A non-2xx status is decoded into an *Error. The request body is
// never logged.
func (c *Client) Patch(ctx context.Context, base, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.writeJSON(ctx, http.MethodPatch, base, path, query, body)
}

// Put issues a PUT to base+path with a JSON-encoded body and returns the raw
// response body on any 2xx status. Google uses PUT to fully replace a resource
// (e.g. Gmail's vacation settings). A non-2xx status is decoded into an *Error.
func (c *Client) Put(ctx context.Context, base, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.writeJSON(ctx, http.MethodPut, base, path, query, body)
}

// Delete issues a DELETE to base+path with no request body and returns the raw
// response body (usually empty, 204 No Content) on any 2xx status. A non-2xx
// status is decoded into an *Error.
func (c *Client) Delete(ctx context.Context, base, path string, query url.Values) (json.RawMessage, error) {
	rawURL, err := c.endpoint(base, path, query)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodDelete, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := statusError(resp); err != nil {
		return nil, err
	}
	return json.RawMessage(resp.body), nil
}

// PostRaw issues a POST sending body verbatim with the given Content-Type — used
// for Drive uploads (a multipart or media body against BaseDriveUpload) that are
// not JSON. It returns the raw response body on any 2xx status; a non-2xx status
// is decoded into an *Error. The request body is never logged.
func (c *Client) PostRaw(ctx context.Context, base, path string, query url.Values, contentType string, body []byte) (json.RawMessage, error) {
	rawURL, err := c.endpoint(base, path, query)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRaw(ctx, http.MethodPost, rawURL, contentType, body)
	if err != nil {
		return nil, err
	}
	if err := statusError(resp); err != nil {
		return nil, err
	}
	return json.RawMessage(resp.body), nil
}

// writeJSON is the shared plumbing behind Post/Patch: it JSON-encodes a non-nil
// body and issues method against base+path through the retrying transport,
// returning the raw response body on any 2xx status.
func (c *Client) writeJSON(ctx context.Context, method, base, path string, query url.Values, body any) (json.RawMessage, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding %s %s body: %w", method, path, err)
		}
		raw = b
	}
	rawURL, err := c.endpoint(base, path, query)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, method, rawURL, raw)
	if err != nil {
		return nil, err
	}
	if err := statusError(resp); err != nil {
		return nil, err
	}
	return json.RawMessage(resp.body), nil
}

// response is the subset of an HTTP response the client acts on.
type response struct {
	status int
	header http.Header
	body   []byte
}

// do issues an authenticated request with an optional JSON body — a thin wrapper
// over doRaw fixing the Content-Type to application/json.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte) (response, error) {
	return c.doRaw(ctx, method, rawURL, "application/json", body)
}

// doRaw issues an authenticated request (method against rawURL, with an optional
// body of the given Content-Type) and returns the response, retrying 429/503 and
// rate-limit 403s per Retry-After (or bounded backoff). A non-nil body is resent
// on each retry. Neither the bearer token nor the request body is ever logged.
func (c *Client) doRaw(ctx context.Context, method, rawURL, contentType string, body []byte) (response, error) {
	token, err := c.ts.GoogleToken(ctx)
	if err != nil {
		return response{}, fmt.Errorf("acquiring Google token: %w", err)
	}

	for attempt := 0; ; attempt++ {
		// A fresh reader each attempt so a retried request resends the body.
		var reqBody io.Reader
		if body != nil {
			reqBody = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
		if err != nil {
			return response{}, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", contentType)
		}

		httpResp, err := c.http.Do(req)
		if err != nil {
			return response{}, fmt.Errorf("calling Google: %w", err)
		}
		respBody, readErr := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes))
		httpResp.Body.Close()
		if readErr != nil {
			return response{}, fmt.Errorf("reading Google response: %w", readErr)
		}

		resp := response{status: httpResp.StatusCode, header: httpResp.Header, body: respBody}

		if retryable(resp) && attempt < maxRetries {
			delay := retryDelay(resp.header.Get("Retry-After"), attempt)
			select {
			case <-ctx.Done():
				return response{}, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}

		return resp, nil
	}
}

// endpoint joins base+path (+query) into an absolute request URL, then rewrites
// the scheme+host when a test override is set. path may be given with or without
// a leading slash.
func (c *Client) endpoint(base, path string, query url.Values) (string, error) {
	raw := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing request URL: %w", err)
	}
	if c.hostOverride != "" {
		ov, err := url.Parse(c.hostOverride)
		if err != nil {
			return "", fmt.Errorf("parsing base override: %w", err)
		}
		u.Scheme = ov.Scheme
		u.Host = ov.Host
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}

// retryable reports whether a response warrants a throttling/availability retry:
// 429 and 503 always, and 403 only when the error reason marks a rate limit
// (Google signals user/project rate limits with a 403 + rateLimitExceeded).
func retryable(resp response) bool {
	switch resp.status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case http.StatusForbidden:
		return isRateLimitReason(reasonOf(resp.body))
	default:
		return false
	}
}

// isRateLimitReason reports whether a Google error reason denotes a retryable
// rate limit rather than a genuine authorization failure.
func isRateLimitReason(reason string) bool {
	switch reason {
	case "rateLimitExceeded", "userRateLimitExceeded":
		return true
	default:
		return false
	}
}

// reasonOf extracts the Google error reason/status from a response body, or ""
// when the body is not an error envelope.
func reasonOf(body []byte) string {
	e := decodeError(body)
	if e == nil {
		return ""
	}
	return e.Reason
}

// retryDelay picks how long to wait before retrying. It prefers the server's
// Retry-After header (delta-seconds); when absent or unparseable it falls back
// to exponential backoff (baseRetryDelay << attempt). Either way the result is
// clamped to [0, maxRetryDelay].
func retryDelay(retryAfter string, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs >= 0 {
		return clampDelay(time.Duration(secs) * time.Second)
	}
	return clampDelay(baseRetryDelay << attempt)
}

// clampDelay bounds a backoff wait to [0, maxRetryDelay].
func clampDelay(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

// statusError returns nil for a 2xx response, otherwise an *Error decoded from
// the Google error envelope. When the body isn't that shape it falls back to the
// HTTP status text. It never includes request credentials.
func statusError(resp response) error {
	if resp.status >= 200 && resp.status < 300 {
		return nil
	}
	if e := decodeError(resp.body); e != nil {
		e.Status = resp.status
		return e
	}
	return &Error{Status: resp.status, Message: http.StatusText(resp.status)}
}

// decodeError parses a Google error envelope into an *Error (without the HTTP
// status, which the caller fills in), or returns nil when the body carries no
// recognizable error. Reason prefers the newer "status" field, falling back to
// the first legacy errors[].reason.
func decodeError(body []byte) *Error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	reason := env.Error.Status
	if reason == "" && len(env.Error.Errors) > 0 {
		reason = env.Error.Errors[0].Reason
	}
	if env.Error.Message == "" && reason == "" {
		return nil
	}
	return &Error{Message: env.Error.Message, Reason: reason}
}
