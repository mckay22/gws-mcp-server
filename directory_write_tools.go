package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDirectoryWriteTools installs the M6 gated Directory mutation tools:
// user lifecycle (create/update/suspend) and group lifecycle (create, add/remove
// member). They register only behind --admin and, being reversible directory
// changes, ride the ordinary write gate (allowWrites) — not the send gate. Google
// enforces the caller's admin privileges on apply.
func registerDirectoryWriteTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerDirectoryUserCreate(server, gc, allowWrites, allowSends)
	registerDirectoryUserUpdate(server, gc, allowWrites, allowSends)
	registerDirectoryUserSuspend(server, gc, allowWrites, allowSends)
	registerDirectoryGroupCreate(server, gc, allowWrites, allowSends)
	registerDirectoryGroupAddMember(server, gc, allowWrites, allowSends)
	registerDirectoryGroupRemoveMember(server, gc, allowWrites, allowSends)
}

// --- directory_user_create ---

type directoryUserCreateInput struct {
	PrimaryEmail string `json:"primaryEmail" jsonschema:"the new user's primary email (required)"`
	GivenName    string `json:"givenName" jsonschema:"first name (required)"`
	FamilyName   string `json:"familyName" jsonschema:"last name (required)"`
	Password     string `json:"password" jsonschema:"initial password (required); redacted in the dry-run preview"`
	OrgUnitPath  string `json:"orgUnitPath,omitempty" jsonschema:"org unit path, e.g. /Sales (default /)"`
}

func registerDirectoryUserCreate(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_user_create",
		Title:       "Create a directory user",
		Description: "Create a new user in the directory (Admin SDK). Reversible admin write, gated by " + config.EnvAllowWrites + " (or --allow-writes). The dry-run preview REDACTS the password. Requires an admin caller with user-management privilege.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryUserCreateInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.PrimaryEmail) == "" || strings.TrimSpace(in.GivenName) == "" || strings.TrimSpace(in.FamilyName) == "" {
			return nil, writeOutput{}, fmt.Errorf("primaryEmail, givenName, and familyName are required")
		}
		if strings.TrimSpace(in.Password) == "" {
			return nil, writeOutput{}, fmt.Errorf("password is required")
		}
		body := map[string]any{
			"primaryEmail": in.PrimaryEmail,
			"name":         map[string]any{"givenName": in.GivenName, "familyName": in.FamilyName},
			"password":     in.Password,
		}
		preview := map[string]any{
			"primaryEmail": in.PrimaryEmail,
			"name":         map[string]any{"givenName": in.GivenName, "familyName": in.FamilyName},
			"password":     "REDACTED",
		}
		if ou := strings.TrimSpace(in.OrgUnitPath); ou != "" {
			body["orgUnitPath"] = ou
			preview["orgUnitPath"] = ou
		}
		plan := writePlan{
			Summary:     fmt.Sprintf("create user %s", in.PrimaryEmail),
			Gate:        gateWrites,
			Method:      "POST",
			Base:        gapi.BaseDirectory,
			Path:        "/users",
			Body:        preview, // applied output also shows the redacted form...
			PreviewBody: preview,
			ApplyBody:   body, // ...while the wire carries the real password
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- directory_user_update ---

type directoryUserUpdateInput struct {
	UserKey     string `json:"userKey" jsonschema:"the user's email or id to update (required)"`
	GivenName   string `json:"givenName,omitempty" jsonschema:"new first name"`
	FamilyName  string `json:"familyName,omitempty" jsonschema:"new last name"`
	OrgUnitPath string `json:"orgUnitPath,omitempty" jsonschema:"move to this org unit path"`
}

func registerDirectoryUserUpdate(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_user_update",
		Title:       "Update a directory user",
		Description: "Patch a user's profile fields (name, org unit) in the directory (Admin SDK PATCH). Reversible admin write gated by " + config.EnvAllowWrites + ". Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryUserUpdateInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.UserKey) == "" {
			return nil, writeOutput{}, fmt.Errorf("userKey is required")
		}
		name := map[string]any{}
		if s := strings.TrimSpace(in.GivenName); s != "" {
			name["givenName"] = s
		}
		if s := strings.TrimSpace(in.FamilyName); s != "" {
			name["familyName"] = s
		}
		body := map[string]any{}
		if len(name) > 0 {
			body["name"] = name
		}
		if s := strings.TrimSpace(in.OrgUnitPath); s != "" {
			body["orgUnitPath"] = s
		}
		if len(body) == 0 {
			return nil, writeOutput{}, fmt.Errorf("provide at least one field to update")
		}
		plan := writePlan{
			Summary: fmt.Sprintf("update user %s", in.UserKey),
			Gate:    gateWrites,
			Method:  "PATCH",
			Base:    gapi.BaseDirectory,
			Path:    "/users/" + url.PathEscape(in.UserKey),
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- directory_user_suspend ---

type directoryUserSuspendInput struct {
	UserKey string `json:"userKey" jsonschema:"the user's email or id (required)"`
	Suspend bool   `json:"suspend" jsonschema:"true to suspend, false to un-suspend"`
}

func registerDirectoryUserSuspend(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_user_suspend",
		Title:       "Suspend or un-suspend a user",
		Description: "Suspend (block sign-in) or un-suspend a directory user (Admin SDK PATCH suspended). Reversible admin write gated by " + config.EnvAllowWrites + ". Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryUserSuspendInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.UserKey) == "" {
			return nil, writeOutput{}, fmt.Errorf("userKey is required")
		}
		verb := "suspend"
		if !in.Suspend {
			verb = "un-suspend"
		}
		plan := writePlan{
			Summary: fmt.Sprintf("%s user %s", verb, in.UserKey),
			Gate:    gateWrites,
			Method:  "PATCH",
			Base:    gapi.BaseDirectory,
			Path:    "/users/" + url.PathEscape(in.UserKey),
			Body:    map[string]any{"suspended": in.Suspend},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- directory_group_create ---

type directoryGroupCreateInput struct {
	Email       string `json:"email" jsonschema:"the new group's email address (required)"`
	Name        string `json:"name,omitempty" jsonschema:"display name"`
	Description string `json:"description,omitempty" jsonschema:"description"`
}

func registerDirectoryGroupCreate(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_group_create",
		Title:       "Create a directory group",
		Description: "Create a new group in the directory (Admin SDK). Reversible admin write gated by " + config.EnvAllowWrites + ". Requires an admin caller with group-management privilege.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryGroupCreateInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.Email) == "" {
			return nil, writeOutput{}, fmt.Errorf("email is required")
		}
		body := map[string]any{"email": in.Email}
		if s := strings.TrimSpace(in.Name); s != "" {
			body["name"] = s
		}
		if s := strings.TrimSpace(in.Description); s != "" {
			body["description"] = s
		}
		plan := writePlan{
			Summary: fmt.Sprintf("create group %s", in.Email),
			Gate:    gateWrites,
			Method:  "POST",
			Base:    gapi.BaseDirectory,
			Path:    "/groups",
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- directory_group_add_member ---

type directoryGroupAddMemberInput struct {
	GroupKey string `json:"groupKey" jsonschema:"the group's email or id (required)"`
	Email    string `json:"email" jsonschema:"the member's email to add (required)"`
	Role     string `json:"role,omitempty" jsonschema:"MEMBER (default), MANAGER, or OWNER"`
}

func registerDirectoryGroupAddMember(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_group_add_member",
		Title:       "Add a group member",
		Description: "Add a member to a directory group with a role (Admin SDK). Reversible admin write gated by " + config.EnvAllowWrites + ". Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryGroupAddMemberInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.GroupKey) == "" || strings.TrimSpace(in.Email) == "" {
			return nil, writeOutput{}, fmt.Errorf("groupKey and email are required")
		}
		role := strings.ToUpper(strings.TrimSpace(in.Role))
		switch role {
		case "", "MEMBER":
			role = "MEMBER"
		case "MANAGER", "OWNER":
		default:
			return nil, writeOutput{}, fmt.Errorf("role must be MEMBER, MANAGER, or OWNER, got %q", in.Role)
		}
		plan := writePlan{
			Summary: fmt.Sprintf("add %s to group %s as %s", in.Email, in.GroupKey, role),
			Gate:    gateWrites,
			Method:  "POST",
			Base:    gapi.BaseDirectory,
			Path:    "/groups/" + url.PathEscape(in.GroupKey) + "/members",
			Body:    map[string]any{"email": in.Email, "role": role},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- directory_group_remove_member ---

type directoryGroupRemoveMemberInput struct {
	GroupKey  string `json:"groupKey" jsonschema:"the group's email or id (required)"`
	MemberKey string `json:"memberKey" jsonschema:"the member's email or id to remove (required)"`
}

func registerDirectoryGroupRemoveMember(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_group_remove_member",
		Title:       "Remove a group member",
		Description: "Remove a member from a directory group (Admin SDK DELETE). Reversible admin write gated by " + config.EnvAllowWrites + ". Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryGroupRemoveMemberInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.GroupKey) == "" || strings.TrimSpace(in.MemberKey) == "" {
			return nil, writeOutput{}, fmt.Errorf("groupKey and memberKey are required")
		}
		plan := writePlan{
			Summary: fmt.Sprintf("remove %s from group %s", in.MemberKey, in.GroupKey),
			Gate:    gateWrites,
			Method:  "DELETE",
			Base:    gapi.BaseDirectory,
			Path:    "/groups/" + url.PathEscape(in.GroupKey) + "/members/" + url.PathEscape(in.MemberKey),
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}
