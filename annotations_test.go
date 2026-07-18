package main

import (
	"context"
	"encoding/json"
	"strings"
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
		"gmail_get_profile", "gmail_list_messages", "gmail_get_message", "gmail_list_labels",
		"calendar_list_events", "calendar_get_event", "calendar_freebusy",
		"drive_list_files", "drive_get_file_content",
		"directory_search_users", "directory_list_role_assignments",
		"admin_list_audit_activities", "admin_list_connected_apps", "admin_list_license_assignments",
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
		"gmail_create_draft":                   false,
		"gmail_send":                           false, // additive: creates a message, destroys nothing
		"gmail_reply":                          false,
		"gmail_modify_labels":                  true, // overwrites label state
		"gmail_set_vacation":                   true, // PUT replaces the whole resource
		"calendar_create_event_with_attendees": false,
		"calendar_update_event":                true,
		"calendar_cancel_event":                true,
		"calendar_respond_to_event":            true,
		"drive_upload_file":                    false,
		"drive_share_file":                     false, // additive to the ACL
		"directory_create_user":                false,
		"directory_update_user":                true,
		"directory_suspend_user":               true,
		"directory_add_group_member":           false,
		"directory_remove_group_member":        true,
		"tasks_create":                         false,
		"tasks_complete":                       true,
		"chat_send_message":                    false,
		"app_send_mail":                        false,
		"app_set_vacation":                     true,
		"app_bulk_user_suspend":                true,
		"app_bulk_group_add_members":           false,
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

// TestEnumInputsAreConstrained checks the enum-shaped inputs advertise their
// permitted values as a JSON Schema `enum`, not only in prose. Prose is not
// machine-readable: a client cannot pre-validate against it, so a wrong value
// reaches Google and comes back as a 400 (or gets silently ignored) instead of
// being caught before the call.
func TestEnumInputsAreConstrained(t *testing.T) {
	tools := listAllTools(t)

	want := map[string]map[string][]string{
		"gmail_get_message":         {"format": {"metadata", "full"}},
		"app_get_message":           {"format": {"metadata", "full"}},
		"drive_share_file":          {"role": {"reader", "commenter", "writer"}, "type": {"user", "group", "domain", "anyone"}},
		"calendar_respond_to_event": {"response": {"accepted", "declined", "tentative"}},
		"directory_search_users":    {"orderBy": {"email", "givenName", "familyName"}},
	}

	for toolName, props := range want {
		tool, ok := tools[toolName]
		if !ok {
			t.Errorf("expected tool %q to be registered", toolName)
			continue
		}
		// The advertised schema is whatever a client receives, so assert on that
		// rather than on the Go type it was inferred from.
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("%s: marshal input schema: %v", toolName, err)
			continue
		}
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("%s: decode input schema: %v", toolName, err)
			continue
		}
		for prop, wantValues := range props {
			got := schema.Properties[prop].Enum
			if len(got) == 0 {
				t.Errorf("%s.%s advertises no enum; a client cannot validate it before calling", toolName, prop)
				continue
			}
			if strings.Join(got, ",") != strings.Join(wantValues, ",") {
				t.Errorf("%s.%s enum = %v, want %v", toolName, prop, got, wantValues)
			}
		}
	}

	// admin_list_audit_activities is checked separately: the list is long and its exact
	// membership is Google's, so only require that it is constrained and includes
	// the applications the description names.
	audit, ok := tools["admin_list_audit_activities"]
	if !ok {
		t.Fatal("admin_list_audit_activities not registered")
	}
	raw, _ := json.Marshal(audit.InputSchema)
	if !strings.Contains(string(raw), `"enum"`) {
		t.Error("admin_list_audit_activities.application advertises no enum")
	}
	for _, app := range []string{"login", "admin", "drive", "token"} {
		if !strings.Contains(string(raw), `"`+app+`"`) {
			t.Errorf("admin_list_audit_activities application enum omits %q, which its description advertises", app)
		}
	}
}
