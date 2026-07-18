package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file holds the application tier's bulk Directory lifecycle tools. Unlike
// the per-user tools they impersonate a configured ADMIN subject (directory
// admin operations need admin rights, not the target's own token). They give
// per-item outcomes and reject duplicate targets outright — a straight port of
// the sibling's app-tier bulk semantics.

// bulkItemResult is the outcome for one target in a bulk operation.
type bulkItemResult struct {
	Target  string `json:"target"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// bulkOutput is the structured result of a bulk operation.
type bulkOutput struct {
	Applied      bool             `json:"applied"`
	DryRun       bool             `json:"dryRun,omitempty"`
	Summary      string           `json:"summary"`
	Actor        string           `json:"actor"`
	Results      []bulkItemResult `json:"results"`
	AppliedCount int              `json:"appliedCount"`
	ErrorCount   int              `json:"errorCount"`
}

// runBulk applies a per-target operation with the write gate, per-item outcomes,
// and duplicate rejection. When the gate is closed it previews every target
// without calling Google. When open it impersonates the admin subject, runs each
// target independently (one failure never aborts the batch), and logs the
// requesting actor once with the applied/error counts. adminCtx must already
// carry the admin impersonation (asUser(ctx, adminSubject)); actor is read from
// the original ctx.
func runBulk(ctx, adminCtx context.Context, allowWrites bool, summary string, targets []string, apply func(context.Context, string) error) (*mcp.CallToolResult, bulkOutput, error) {
	if len(targets) == 0 {
		return nil, bulkOutput{}, fmt.Errorf("at least one target is required")
	}
	// Duplicate rejection: the whole call fails rather than silently acting twice.
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if t == "" {
			return nil, bulkOutput{}, fmt.Errorf("targets must not be blank")
		}
		if seen[t] {
			return nil, bulkOutput{}, fmt.Errorf("duplicate target %q — refusing an ambiguous bulk request", t)
		}
		seen[t] = true
	}

	actor := actorFromContext(ctx)

	if !allowWrites {
		results := make([]bulkItemResult, 0, len(targets))
		for _, t := range targets {
			results = append(results, bulkItemResult{Target: strings.TrimSpace(t)})
		}
		out := bulkOutput{
			DryRun:  true,
			Summary: summary,
			Actor:   actor,
			Results: results,
		}
		msg := fmt.Sprintf("DRY RUN — would %s for %d targets (set %s=true or pass --allow-writes to apply)",
			summary, len(targets), config.EnvAllowWrites)
		return text(msg), out, nil
	}

	results := make([]bulkItemResult, 0, len(targets))
	var applied, errored int
	for _, t := range targets {
		t = strings.TrimSpace(t)
		if err := apply(adminCtx, t); err != nil {
			results = append(results, bulkItemResult{Target: t, Error: toolError(err).Error()})
			errored++
			continue
		}
		results = append(results, bulkItemResult{Target: t, Applied: true})
		applied++
	}

	slog.Info("app-tier bulk mutation applied",
		"actor", actor, "action", summary, "applied", applied, "errors", errored)

	out := bulkOutput{
		Applied:      true,
		Summary:      summary,
		Actor:        actor,
		Results:      results,
		AppliedCount: applied,
		ErrorCount:   errored,
	}
	return text(fmt.Sprintf("%s: %d applied, %d errors (actor %s)", summary, applied, errored, actor)), out, nil
}

// requireAdminSubject returns the configured admin subject or an error naming the
// variable to set.
func requireAdminSubject(cfg config.Config) (string, error) {
	if s := strings.TrimSpace(cfg.AppAdminSubject); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("bulk directory operations require %s (the admin user the application SA impersonates)", config.EnvAppAdminSubject)
}

// --- app_bulk_user_suspend ---

type appBulkUserSuspendInput struct {
	Users   []string `json:"users" jsonschema:"the user emails/ids to suspend or un-suspend (required; duplicates are rejected)"`
	Suspend bool     `json:"suspend" jsonschema:"true to suspend, false to un-suspend"`
}

func registerAppBulkUserSuspend(server *mcp.Server, gc *gapi.Client, cfg config.Config) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_bulk_user_suspend",
		Annotations: destructiveAnnotations(),
		Title:       "App: bulk suspend/un-suspend users",
		Description: "Suspend or un-suspend many directory users in one call via the application service account (impersonating the configured admin). Reversible, so it rides the write gate. Returns per-user outcomes; duplicate targets are rejected. Applied batches are logged with the requesting actor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appBulkUserSuspendInput) (*mcp.CallToolResult, bulkOutput, error) {
		adminSubject, err := requireAdminSubject(cfg)
		if err != nil {
			return nil, bulkOutput{}, err
		}
		verb := "suspend"
		if !in.Suspend {
			verb = "un-suspend"
		}
		summary := fmt.Sprintf("%s %d users", verb, len(in.Users))
		adminCtx := asUser(ctx, adminSubject)
		return runBulk(ctx, adminCtx, cfg.AllowWrites, summary, in.Users, func(c context.Context, user string) error {
			_, err := gc.Patch(c, gapi.BaseDirectory, "/users/"+url.PathEscape(user), nil, map[string]any{"suspended": in.Suspend})
			return err
		})
	})
}

// --- app_bulk_group_add_members ---

type appBulkGroupAddInput struct {
	GroupKey string   `json:"groupKey" jsonschema:"the group email/id to add members to (required)"`
	Members  []string `json:"members" jsonschema:"the member emails to add (required; duplicates are rejected)"`
	Role     string   `json:"role,omitempty" jsonschema:"MEMBER (default), MANAGER, or OWNER"`
}

func registerAppBulkGroupAddMembers(server *mcp.Server, gc *gapi.Client, cfg config.Config) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "app_bulk_group_add_members",
		Annotations: additiveAnnotations(),
		Title:       "App: bulk add group members",
		Description: "Add many members to a directory group in one call via the application service account (impersonating the configured admin). Reversible, so it rides the write gate. Returns per-member outcomes; duplicate members are rejected. Applied batches are logged with the requesting actor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in appBulkGroupAddInput) (*mcp.CallToolResult, bulkOutput, error) {
		if strings.TrimSpace(in.GroupKey) == "" {
			return nil, bulkOutput{}, fmt.Errorf("groupKey is required")
		}
		adminSubject, err := requireAdminSubject(cfg)
		if err != nil {
			return nil, bulkOutput{}, err
		}
		role := strings.ToUpper(strings.TrimSpace(in.Role))
		switch role {
		case "", "MEMBER":
			role = "MEMBER"
		case "MANAGER", "OWNER":
		default:
			return nil, bulkOutput{}, fmt.Errorf("role must be MEMBER, MANAGER, or OWNER, got %q", in.Role)
		}
		summary := fmt.Sprintf("add %d members to group %s as %s", len(in.Members), in.GroupKey, role)
		adminCtx := asUser(ctx, adminSubject)
		return runBulk(ctx, adminCtx, cfg.AllowWrites, summary, in.Members, func(c context.Context, member string) error {
			_, err := gc.Post(c, gapi.BaseDirectory, "/groups/"+url.PathEscape(in.GroupKey)+"/members", nil, map[string]any{"email": member, "role": role})
			return err
		})
	})
}
