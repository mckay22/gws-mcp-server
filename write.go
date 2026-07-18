package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gate names which of the two independent gates guards a mutation. Writes are
// the ordinary reversible mutations (--allow-writes); sends are the irreversible
// or egress actions (--allow-sends) — mail sending, sharing — and opening the
// write gate never opens the send gate.
type gate int

const (
	gateWrites gate = iota
	gateSends
)

// writePlan is a single planned Google API mutation. A write tool builds one and
// hands it to runWrite, which either previews it (the relevant gate closed) or
// applies it (gate open) — so the plan is the one description of the request that
// the dry-run and the real call share.
type writePlan struct {
	// Summary is a human-readable one-liner naming the mutation, e.g.
	// "send mail to ada@example.com". It fronts both the preview and applied text.
	Summary string
	// Gate selects which gate guards this mutation (gateWrites or gateSends).
	Gate gate
	// Method is the HTTP verb: POST, PATCH, or DELETE.
	Method string
	// Base is the gapi service base (BaseGmail, BaseCalendar, BaseDrive, …).
	Base string
	// Path is the resource path under Base, e.g. "/users/me/messages/send".
	Path string
	// Query is the optional query attached to the request, e.g. sendUpdates=all.
	Query url.Values
	// Body is the request payload shown in the preview and the applied output.
	// For Gmail sends it is a readable to/subject/body map, NOT the wire form.
	Body any
	// PreviewBody, when non-nil, is shown in the DRY-RUN instead of Body — used to
	// redact a secret from the preview.
	PreviewBody any
	// ApplyBody, when non-nil, is the JSON payload actually sent on apply instead
	// of Body — used when the wire form differs from the readable display form
	// (e.g. Gmail's base64url raw MIME). Nil means send Body.
	ApplyBody any
	// RawBody, when non-nil, is sent verbatim on apply with RawContentType (via
	// PostRaw) instead of a JSON body — Drive's multipart upload. Only POST uses
	// it; Body still carries the readable description for preview/output.
	RawBody        []byte
	RawContentType string
	// Prepare, when non-nil, computes the request body at apply time, replacing
	// Body/ApplyBody. It runs ONLY once the gate is open, so a dry run still makes
	// no Google call.
	//
	// It exists for the mutations that must read current state before writing it:
	// Google's PATCH overwrites array fields wholesale, so changing one element of
	// an array (an RSVP among an event's attendees) means fetching the array,
	// editing the one entry, and sending it back intact. Body still carries the
	// readable intent for the preview.
	Prepare func(ctx context.Context) (any, error)
}

// writeOutput is the structured result of a write tool: the planned request plus
// whether it was applied or only previewed.
type writeOutput struct {
	Applied bool   `json:"applied"`
	DryRun  bool   `json:"dryRun,omitempty"`
	Summary string `json:"summary"`
	Method  string `json:"method"`
	URL     string `json:"url"`
	Body    any    `json:"body,omitempty"`
	Result  string `json:"result,omitempty"`
}

// runWrite applies the appropriate gate to a plan.
//
// When the plan's gate is closed it makes NO Google call: it returns a dry-run
// preview (DryRun=true, Applied=false) whose Body is plan.PreviewBody when set,
// otherwise plan.Body — so a secret in Body is never echoed back. The message
// names the exact env var and flag that open THIS plan's gate.
//
// When the gate is open it dispatches on plan.Method to the matching client write
// (Post/PostRaw/Patch/Delete) and returns Applied=true. A failure surfaces as the
// client's *gapi.Error, unchanged.
func runWrite(ctx context.Context, gc *gapi.Client, allowWrites, allowSends bool, plan writePlan) (*mcp.CallToolResult, writeOutput, error) {
	allowed, gateEnv, gateFlag := gateSettings(plan.Gate, allowWrites, allowSends)
	displayURL := planURL(plan)

	if !allowed {
		body := plan.Body
		if plan.PreviewBody != nil {
			body = plan.PreviewBody
		}
		out := writeOutput{
			DryRun:  true,
			Summary: plan.Summary,
			Method:  plan.Method,
			URL:     displayURL,
			Body:    body,
		}
		msg := fmt.Sprintf("DRY RUN — would %s %s (set %s=true or pass %s to apply)",
			plan.Method, displayURL, gateEnv, gateFlag)
		return text(msg), out, nil
	}

	out := writeOutput{
		Applied: true,
		Summary: plan.Summary,
		Method:  plan.Method,
		URL:     displayURL,
		Body:    plan.Body,
	}

	applyBody := plan.Body
	if plan.ApplyBody != nil {
		applyBody = plan.ApplyBody
	}
	if plan.Prepare != nil {
		// Read-modify-write: build the body from current state now that the gate is
		// open. A failure here aborts before any mutation is sent.
		prepared, err := plan.Prepare(ctx)
		if err != nil {
			return nil, writeOutput{}, err
		}
		applyBody = prepared
		out.Body = prepared // report what was actually sent, not the intent
	}

	var (
		raw []byte
		err error
	)
	switch plan.Method {
	case http.MethodPost:
		if plan.RawBody != nil {
			raw, err = gc.PostRaw(ctx, plan.Base, plan.Path, plan.Query, plan.RawContentType, plan.RawBody)
		} else {
			raw, err = gc.Post(ctx, plan.Base, plan.Path, plan.Query, applyBody)
		}
	case http.MethodPatch:
		raw, err = gc.Patch(ctx, plan.Base, plan.Path, plan.Query, applyBody)
	case http.MethodPut:
		raw, err = gc.Put(ctx, plan.Base, plan.Path, plan.Query, applyBody)
	case http.MethodDelete:
		raw, err = gc.Delete(ctx, plan.Base, plan.Path, plan.Query)
	default:
		return nil, writeOutput{}, fmt.Errorf("unsupported write method %q", plan.Method)
	}
	if err != nil {
		return nil, writeOutput{}, err
	}
	if len(raw) > 0 {
		out.Result = string(raw)
	}

	return text("Applied: " + plan.Summary), out, nil
}

// gateSettings resolves a plan's gate to whether it is open and the env var /
// flag that open it, so the dry-run message is always accurate for the gate the
// plan actually uses.
func gateSettings(g gate, allowWrites, allowSends bool) (allowed bool, env, flag string) {
	if g == gateSends {
		return allowSends, config.EnvAllowSends, "--allow-sends"
	}
	return allowWrites, config.EnvAllowWrites, "--allow-writes"
}

// planURL renders the human-facing exact request URL (real service host + path +
// query) for the preview and applied output.
func planURL(plan writePlan) string {
	u := strings.TrimRight(plan.Base, "/") + "/" + strings.TrimLeft(plan.Path, "/")
	if len(plan.Query) > 0 {
		u += "?" + plan.Query.Encode()
	}
	return u
}
