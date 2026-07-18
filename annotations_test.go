package main

import (
	"context"
	"testing"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listAllTools registers the entire surface — every tier, both gates open, so
// nothing is filtered out — and returns the advertised tools by name.
func listAllTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	cfg := config.Config{
		AllowWrites: true,
		AllowSends:  true,
		Admin:       true,
		Powerful:    true,
	}
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: version}, nil)
	registerHealth(server, cfg, "stdio")
	gc := gapi.New(&appTokenSource{})
	registerTools(server, gc, cfg)
	// The application tier is registered directly: newMCPServer would demand a
	// real service-account key on disk, and the annotations do not depend on it.
	registerAppTools(server, gc, cfg)

	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "t"}, nil).Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

// TestEveryToolIsAnnotated enforces the contract a client — or a policy layer in
// front of one — depends on: no tool may ship without hints. The spec treats an
// unannotated tool as read-write, destructive and non-idempotent, so a silent
// omission would quietly mark a mailbox read as dangerous (or, worse, let a
// reviewer assume the surface is self-describing when part of it is not).
func TestEveryToolIsAnnotated(t *testing.T) {
	tools := listAllTools(t)
	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}
	for name, tool := range tools {
		if tool.Annotations == nil {
			t.Errorf("tool %q declares no annotations", name)
			continue
		}
		if tool.Annotations.OpenWorldHint == nil {
			t.Errorf("tool %q does not declare openWorldHint", name)
		}
		if tool.Annotations.ReadOnlyHint {
			// A read-only tool changes nothing, so claiming destructiveness would
			// be contradictory. (The spec says destructiveHint is meaningful only
			// when readOnlyHint is false.)
			if d := tool.Annotations.DestructiveHint; d != nil && *d {
				t.Errorf("tool %q is readOnly but claims destructiveHint=true", name)
			}
		} else if tool.Annotations.DestructiveHint == nil {
			t.Errorf("mutating tool %q does not declare destructiveHint", name)
		}
	}
}

// TestToolAnnotationsMatchBehavior pins the classification of the tools whose
// risk a caller most needs to get right: reads must be marked read-only, and the
// mutations must not be. A tool moving between these sets is a real behavioral
// change and should have to be stated here.
func TestToolAnnotationsMatchBehavior(t *testing.T) {
	tools := listAllTools(t)

	readOnly := []string{
		"health",
		"get_profile", "list_messages", "get_message", "list_labels",
		"list_events", "get_event", "freebusy_query",
		"list_files", "get_file_content",
		"directory_users_search", "directory_role_assignments",
		"audit_activities", "user_connected_apps", "license_assignments",
		"app_list_messages", "app_get_message", "app_list_files",
	}
	for _, name := range readOnly {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read tool %q is not marked readOnly", name)
		}
	}

	// Every mutation must be marked read-write; the destructive ones must say so.
	mutating := map[string]bool{ // tool -> expected destructiveHint
		"gmail_create_draft":            false,
		"gmail_send":                    false, // additive: creates a message, destroys nothing
		"gmail_reply":                   false,
		"gmail_modify":                  true, // overwrites label state
		"gmail_set_vacation":            true, // PUT replaces the whole resource
		"create_event_with_attendees":   false,
		"update_event":                  true,
		"cancel_event":                  true,
		"respond_to_event":              true,
		"upload_file":                   false,
		"share_file":                    false, // additive to the ACL
		"directory_user_create":         false,
		"directory_user_update":         true,
		"directory_user_suspend":        true,
		"directory_group_add_member":    false,
		"directory_group_remove_member": true,
		"tasks_create":                  false,
		"tasks_complete":                true,
		"chat_send_message":             false,
		"app_send_mail":                 false,
		"app_set_vacation":              true,
		"app_bulk_user_suspend":         true,
		"app_bulk_group_add_members":    false,
	}
	for name, wantDestructive := range mutating {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("expected tool %q to be registered", name)
			continue
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("mutating tool %q is marked readOnly", name)
			continue
		}
		got := tool.Annotations.DestructiveHint
		if got == nil {
			t.Errorf("mutating tool %q declares no destructiveHint", name)
			continue
		}
		if *got != wantDestructive {
			t.Errorf("tool %q destructiveHint = %v, want %v", name, *got, wantDestructive)
		}
	}
}

// TestHealthIsClosedWorld checks the one tool that makes no Google call is the
// one tool marked closed-world; everything else reaches an external service.
func TestHealthIsClosedWorld(t *testing.T) {
	tools := listAllTools(t)
	for name, tool := range tools {
		open := tool.Annotations.OpenWorldHint
		if open == nil {
			continue // reported by TestEveryToolIsAnnotated
		}
		if name == "health" {
			if *open {
				t.Error("health makes no Google call but is marked open-world")
			}
			continue
		}
		if !*open {
			t.Errorf("tool %q calls Google but is marked closed-world", name)
		}
	}
}
