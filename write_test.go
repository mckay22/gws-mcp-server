package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type writeCapture struct {
	mu     sync.Mutex
	called bool
	method string
	path   string
	body   string
}

// writeMock records the single request it receives and returns a created
// resource. Tests assert on the capture (or that it was never called).
func writeMock(t *testing.T) (*gapi.Client, *writeCapture) {
	t.Helper()
	cap := &writeCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.mu.Lock()
		cap.called = true
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.body = string(b)
		cap.mu.Unlock()
		writeJSON(w, http.StatusOK, `{"id":"created-id"}`)
	}))
	t.Cleanup(srv.Close)
	return gapi.New(fakeTS{}, gapi.WithBaseURL(srv.URL)), cap
}

func samplePlan() writePlan {
	return writePlan{
		Summary: "send mail to ada@example.com",
		Gate:    gateWrites,
		Method:  http.MethodPost,
		Base:    gapi.BaseGmail,
		Path:    "/users/me/messages/send",
		Body:    map[string]any{"to": "ada@example.com"},
	}
}

func TestRunWriteDryRunMakesNoCall(t *testing.T) {
	gc, cap := writeMock(t)
	_, out, err := runWrite(context.Background(), gc, false, false, samplePlan())
	if err != nil {
		t.Fatalf("runWrite: %v", err)
	}
	if !out.DryRun || out.Applied {
		t.Errorf("expected dry-run, got %+v", out)
	}
	if cap.called {
		t.Error("dry-run must not make an HTTP call")
	}
	if !strings.Contains(out.URL, "gmail.googleapis.com") {
		t.Errorf("preview URL = %q, want the real service host", out.URL)
	}
}

func TestRunWriteWriteGateMessageNamesWriteGate(t *testing.T) {
	gc, _ := writeMock(t)
	res, _, _ := runWrite(context.Background(), gc, false, false, samplePlan())
	msg := resultText(res)
	if !strings.Contains(msg, "GWS_MCP_ALLOW_WRITES") || !strings.Contains(msg, "--allow-writes") {
		t.Errorf("write-gate message = %q, want it to name the write gate", msg)
	}
}

func TestRunWriteSendGateMessageNamesSendGate(t *testing.T) {
	gc, _ := writeMock(t)
	plan := samplePlan()
	plan.Gate = gateSends
	res, _, _ := runWrite(context.Background(), gc, false, false, plan)
	msg := resultText(res)
	if !strings.Contains(msg, "GWS_MCP_ALLOW_SENDS") || !strings.Contains(msg, "--allow-sends") {
		t.Errorf("send-gate message = %q, want it to name the send gate", msg)
	}
	if strings.Contains(msg, "ALLOW_WRITES") {
		t.Errorf("send-gate message must not mention the write gate: %q", msg)
	}
}

// TestRunWriteGateIndependence is the M3 acceptance bar: the write gate and the
// send gate are fully independent in both directions.
func TestRunWriteGateIndependence(t *testing.T) {
	writePlan := samplePlan() // gateWrites
	sendPlan := samplePlan()
	sendPlan.Gate = gateSends

	t.Run("writes open, sends closed", func(t *testing.T) {
		// A write applies; a send does NOT.
		gc, cap := writeMock(t)
		_, wOut, _ := runWrite(context.Background(), gc, true, false, writePlan)
		if !wOut.Applied {
			t.Error("write should apply when the write gate is open")
		}
		if !cap.called {
			t.Error("write should have made a call")
		}

		gc2, cap2 := writeMock(t)
		_, sOut, _ := runWrite(context.Background(), gc2, true, false, sendPlan)
		if sOut.Applied || !sOut.DryRun {
			t.Error("send must NOT apply when only the write gate is open")
		}
		if cap2.called {
			t.Error("send must not make a call when the send gate is closed")
		}
	})

	t.Run("sends open, writes closed", func(t *testing.T) {
		// A send applies; a write does NOT.
		gc, cap := writeMock(t)
		_, sOut, _ := runWrite(context.Background(), gc, false, true, sendPlan)
		if !sOut.Applied {
			t.Error("send should apply when the send gate is open")
		}
		if !cap.called {
			t.Error("send should have made a call")
		}

		gc2, cap2 := writeMock(t)
		_, wOut, _ := runWrite(context.Background(), gc2, false, true, writePlan)
		if wOut.Applied || !wOut.DryRun {
			t.Error("write must NOT apply when only the send gate is open")
		}
		if cap2.called {
			t.Error("write must not make a call when the write gate is closed")
		}
	})
}

func TestRunWriteAppliesPost(t *testing.T) {
	gc, cap := writeMock(t)
	_, out, err := runWrite(context.Background(), gc, true, false, samplePlan())
	if err != nil {
		t.Fatalf("runWrite: %v", err)
	}
	if !out.Applied {
		t.Errorf("expected applied, got %+v", out)
	}
	if out.Result != `{"id":"created-id"}` {
		t.Errorf("result = %q", out.Result)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.method != http.MethodPost || cap.path != "/gmail/v1/users/me/messages/send" {
		t.Errorf("recorded %s %s", cap.method, cap.path)
	}
}

func TestRunWritePreviewBodyRedacts(t *testing.T) {
	gc, _ := writeMock(t)
	plan := samplePlan()
	plan.Body = map[string]any{"secret": "top-secret-value"}
	plan.PreviewBody = map[string]any{"secret": "REDACTED"}
	_, out, _ := runWrite(context.Background(), gc, false, false, plan)
	body := out.Body.(map[string]any)
	if body["secret"] != "REDACTED" {
		t.Errorf("dry-run body = %v, want the redacted preview body", body)
	}
}

func TestRunWriteApplyBodyOverridesWireForm(t *testing.T) {
	gc, cap := writeMock(t)
	plan := samplePlan()
	plan.Body = map[string]any{"readable": "to: ada, subject: hi"}
	plan.ApplyBody = map[string]any{"raw": "encoded-mime-blob"}
	_, out, err := runWrite(context.Background(), gc, true, false, plan)
	if err != nil {
		t.Fatalf("runWrite: %v", err)
	}
	// The applied output still shows the readable Body...
	if b := out.Body.(map[string]any); b["readable"] == nil {
		t.Errorf("applied output body = %v, want the readable form", out.Body)
	}
	// ...but the wire actually carried ApplyBody.
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if !strings.Contains(cap.body, "encoded-mime-blob") {
		t.Errorf("sent body = %q, want the ApplyBody wire form", cap.body)
	}
	if strings.Contains(cap.body, "readable") {
		t.Errorf("sent body leaked the display form: %q", cap.body)
	}
}

func TestRunWriteSurfacesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, `{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`)
	}))
	defer srv.Close()
	gc := gapi.New(fakeTS{}, gapi.WithBaseURL(srv.URL))

	_, _, err := runWrite(context.Background(), gc, true, false, samplePlan())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("error = %v", err)
	}
}

// resultText concatenates the text content of a CallToolResult.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
