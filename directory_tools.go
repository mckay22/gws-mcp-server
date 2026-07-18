package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDirectoryReadTools installs the M4 read-only Admin SDK Directory tools.
// They register only when --admin (GWS_MCP_ADMIN) is set, because they need the
// signed-in user to hold a Workspace/Cloud Identity admin role and request
// sensitive admin scopes. Google still enforces the caller's admin privileges on
// every call — this server adds no permission model.
func registerDirectoryReadTools(server *mcp.Server, gc *gapi.Client) {
	registerDirectoryUsersSearch(server, gc)
	registerDirectoryUserGet(server, gc)
	registerDirectoryGroupsSearch(server, gc)
	registerDirectoryGroupMembers(server, gc)
	registerDirectoryRolesList(server, gc)
	registerDirectoryRoleAssignments(server, gc)
}

// myCustomer is the Directory API's alias for the caller's own account/customer.
const myCustomer = "my_customer"

// fields projections keep Directory responses to what each tool surfaces.
const (
	userSummaryFields = "users(id,primaryEmail,name/fullName,isAdmin,isDelegatedAdmin,suspended,orgUnitPath,lastLoginTime),nextPageToken"
	userDetailFields  = "id,primaryEmail,name(fullName,givenName,familyName),isAdmin,isDelegatedAdmin,suspended,archived,orgUnitPath,aliases,creationTime,lastLoginTime,isEnrolledIn2Sv"
	groupFields       = "groups(id,email,name,description,directMembersCount,adminCreated),nextPageToken"
	memberFields      = "members(id,email,role,type,status),nextPageToken"
	roleFields        = "items(roleId,roleName,roleDescription,isSystemRole,isSuperAdminRole),nextPageToken"
	roleAssignFields  = "items(roleAssignmentId,roleId,assignedTo,scopeType,orgUnitId),nextPageToken"
)

// --- directory_users_search ---

type directoryUsersSearchInput struct {
	Query      string `json:"query,omitempty" jsonschema:"Admin SDK users query (e.g. \"email:ada*\", \"name:Ada\", \"orgUnitPath=/Sales\", \"isAdmin=true\"); omit to list all users"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25; API max 500)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
	OrderBy    string `json:"orderBy,omitempty" jsonschema:"sort field: email (default), givenName, or familyName"`
}

// DirectoryUserSummary is the compact user shape returned by search.
type DirectoryUserSummary struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
	Name         struct {
		FullName string `json:"fullName"`
	} `json:"name"`
	IsAdmin          bool   `json:"isAdmin,omitempty"`
	IsDelegatedAdmin bool   `json:"isDelegatedAdmin,omitempty"`
	Suspended        bool   `json:"suspended,omitempty"`
	OrgUnitPath      string `json:"orgUnitPath,omitempty"`
	LastLoginTime    string `json:"lastLoginTime,omitempty"`
}

type directoryUsersSearchOutput struct {
	Users         []DirectoryUserSummary `json:"users"`
	Count         int                    `json:"count"`
	NextPageToken string                 `json:"nextPageToken,omitempty"`
}

func registerDirectoryUsersSearch(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_users_search",
		Annotations: readAnnotations(),
		Title:       "Search directory users",
		Description: "Search or list users in the Workspace/Cloud Identity directory (Admin SDK). Requires the signed-in user to be an admin. Returns compact summaries; page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryUsersSearchInput) (*mcp.CallToolResult, directoryUsersSearchOutput, error) {
		q := url.Values{}
		q.Set("customer", myCustomer)
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", userSummaryFields)
		q.Set("projection", "basic")
		if s := strings.TrimSpace(in.Query); s != "" {
			q.Set("query", s)
		}
		if o := strings.TrimSpace(in.OrderBy); o != "" {
			q.Set("orderBy", o)
		} else {
			q.Set("orderBy", "email")
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/users", q)
		if err != nil {
			return nil, directoryUsersSearchOutput{}, toolError(err)
		}
		var env struct {
			Users         []DirectoryUserSummary `json:"users"`
			NextPageToken string                 `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, directoryUsersSearchOutput{}, fmt.Errorf("decoding users: %w", err)
		}
		out := directoryUsersSearchOutput{Users: env.Users, Count: len(env.Users), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d users", out.Count)), out, nil
	})
}

// --- directory_user_get ---

type directoryUserGetInput struct {
	UserKey string `json:"userKey" jsonschema:"the user's primary email or immutable id (required)"`
}

// DirectoryUserDetail is the fuller user shape returned by directory_user_get.
type DirectoryUserDetail struct {
	ID           string `json:"id"`
	PrimaryEmail string `json:"primaryEmail"`
	Name         struct {
		FullName   string `json:"fullName,omitempty"`
		GivenName  string `json:"givenName,omitempty"`
		FamilyName string `json:"familyName,omitempty"`
	} `json:"name"`
	IsAdmin          bool     `json:"isAdmin,omitempty"`
	IsDelegatedAdmin bool     `json:"isDelegatedAdmin,omitempty"`
	Suspended        bool     `json:"suspended,omitempty"`
	Archived         bool     `json:"archived,omitempty"`
	OrgUnitPath      string   `json:"orgUnitPath,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	CreationTime     string   `json:"creationTime,omitempty"`
	LastLoginTime    string   `json:"lastLoginTime,omitempty"`
	IsEnrolledIn2Sv  bool     `json:"isEnrolledIn2Sv,omitempty"`
}

func registerDirectoryUserGet(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_user_get",
		Annotations: readAnnotations(),
		Title:       "Get directory user",
		Description: "Fetch a single directory user by primary email or id (Admin SDK), including aliases, org unit, admin flags, and 2SV enrollment. Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryUserGetInput) (*mcp.CallToolResult, DirectoryUserDetail, error) {
		if strings.TrimSpace(in.UserKey) == "" {
			return nil, DirectoryUserDetail{}, fmt.Errorf("userKey is required")
		}
		q := url.Values{"fields": {userDetailFields}, "projection": {"basic"}}
		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/users/"+url.PathEscape(in.UserKey), q)
		if err != nil {
			return nil, DirectoryUserDetail{}, toolError(err)
		}
		var u DirectoryUserDetail
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil, DirectoryUserDetail{}, fmt.Errorf("decoding user: %w", err)
		}
		return text(fmt.Sprintf("%s (%s)", u.Name.FullName, u.PrimaryEmail)), u, nil
	})
}

// --- directory_groups_search ---

type directoryGroupsSearchInput struct {
	Query      string `json:"query,omitempty" jsonschema:"Admin SDK groups query (e.g. \"email:eng*\", \"name:Engineering\"); omit to list all groups"`
	UserKey    string `json:"userKey,omitempty" jsonschema:"list only groups this user (email/id) is a member of"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25; API max 200)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// DirectoryGroup is the compact group shape returned by search.
type DirectoryGroup struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	Name               string `json:"name,omitempty"`
	Description        string `json:"description,omitempty"`
	DirectMembersCount string `json:"directMembersCount,omitempty"`
	AdminCreated       bool   `json:"adminCreated,omitempty"`
}

type directoryGroupsSearchOutput struct {
	Groups        []DirectoryGroup `json:"groups"`
	Count         int              `json:"count"`
	NextPageToken string           `json:"nextPageToken,omitempty"`
}

func registerDirectoryGroupsSearch(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_groups_search",
		Annotations: readAnnotations(),
		Title:       "Search directory groups",
		Description: "Search or list groups in the directory (Admin SDK), optionally limited to those a given user belongs to. Requires an admin caller. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryGroupsSearchInput) (*mcp.CallToolResult, directoryGroupsSearchOutput, error) {
		q := url.Values{}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", groupFields)
		// userKey and customer are mutually exclusive on the groups.list call.
		if uk := strings.TrimSpace(in.UserKey); uk != "" {
			q.Set("userKey", uk)
		} else {
			q.Set("customer", myCustomer)
		}
		if s := strings.TrimSpace(in.Query); s != "" {
			q.Set("query", s)
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/groups", q)
		if err != nil {
			return nil, directoryGroupsSearchOutput{}, toolError(err)
		}
		var env struct {
			Groups        []DirectoryGroup `json:"groups"`
			NextPageToken string           `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, directoryGroupsSearchOutput{}, fmt.Errorf("decoding groups: %w", err)
		}
		out := directoryGroupsSearchOutput{Groups: env.Groups, Count: len(env.Groups), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d groups", out.Count)), out, nil
	})
}

// --- directory_group_members ---

type directoryGroupMembersInput struct {
	GroupKey   string `json:"groupKey" jsonschema:"the group's email or id (required)"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25; API max 200)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// DirectoryMember is a compact group membership entry.
type DirectoryMember struct {
	ID     string `json:"id"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
}

type directoryGroupMembersOutput struct {
	Members       []DirectoryMember `json:"members"`
	Count         int               `json:"count"`
	NextPageToken string            `json:"nextPageToken,omitempty"`
}

func registerDirectoryGroupMembers(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_group_members",
		Annotations: readAnnotations(),
		Title:       "List group members",
		Description: "List the members of a directory group by group email or id (Admin SDK), with each member's role (OWNER/MANAGER/MEMBER) and type. Requires an admin caller. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryGroupMembersInput) (*mcp.CallToolResult, directoryGroupMembersOutput, error) {
		if strings.TrimSpace(in.GroupKey) == "" {
			return nil, directoryGroupMembersOutput{}, fmt.Errorf("groupKey is required")
		}
		q := url.Values{}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", memberFields)
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/groups/"+url.PathEscape(in.GroupKey)+"/members", q)
		if err != nil {
			return nil, directoryGroupMembersOutput{}, toolError(err)
		}
		var env struct {
			Members       []DirectoryMember `json:"members"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, directoryGroupMembersOutput{}, fmt.Errorf("decoding members: %w", err)
		}
		out := directoryGroupMembersOutput{Members: env.Members, Count: len(env.Members), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d members", out.Count)), out, nil
	})
}

// --- directory_roles_list ---

type directoryRolesListInput struct {
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// DirectoryRole is a compact admin role definition.
type DirectoryRole struct {
	RoleID           string `json:"roleId"`
	RoleName         string `json:"roleName"`
	RoleDescription  string `json:"roleDescription,omitempty"`
	IsSystemRole     bool   `json:"isSystemRole,omitempty"`
	IsSuperAdminRole bool   `json:"isSuperAdminRole,omitempty"`
}

type directoryRolesListOutput struct {
	Roles         []DirectoryRole `json:"roles"`
	Count         int             `json:"count"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

func registerDirectoryRolesList(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_roles_list",
		Annotations: readAnnotations(),
		Title:       "List admin roles",
		Description: "List the admin roles defined for the account (Admin SDK role management) — system roles and custom roles, flagging super-admin. Requires an admin caller with the role-management privilege.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryRolesListInput) (*mcp.CallToolResult, directoryRolesListOutput, error) {
		q := url.Values{}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", roleFields)
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/customer/"+myCustomer+"/roles", q)
		if err != nil {
			return nil, directoryRolesListOutput{}, toolError(err)
		}
		var env struct {
			Items         []DirectoryRole `json:"items"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, directoryRolesListOutput{}, fmt.Errorf("decoding roles: %w", err)
		}
		out := directoryRolesListOutput{Roles: env.Items, Count: len(env.Items), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d admin roles", out.Count)), out, nil
	})
}

// --- directory_role_assignments ---

type directoryRoleAssignmentsInput struct {
	UserKey    string `json:"userKey,omitempty" jsonschema:"limit to assignments for this user (email/id)"`
	RoleID     string `json:"roleId,omitempty" jsonschema:"limit to assignments of this role id"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// DirectoryRoleAssignment is a compact admin-role assignment.
type DirectoryRoleAssignment struct {
	RoleAssignmentID string `json:"roleAssignmentId"`
	RoleID           string `json:"roleId"`
	AssignedTo       string `json:"assignedTo"`
	ScopeType        string `json:"scopeType,omitempty"`
	OrgUnitID        string `json:"orgUnitId,omitempty"`
}

type directoryRoleAssignmentsOutput struct {
	Assignments   []DirectoryRoleAssignment `json:"assignments"`
	Count         int                       `json:"count"`
	NextPageToken string                    `json:"nextPageToken,omitempty"`
}

func registerDirectoryRoleAssignments(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "directory_role_assignments",
		Annotations: readAnnotations(),
		Title:       "List admin role assignments",
		Description: "List admin-role assignments (Admin SDK), optionally filtered to a user or a role id — who holds which admin role. assignedTo is a user id (resolve via directory_user_get). Requires an admin caller.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in directoryRoleAssignmentsInput) (*mcp.CallToolResult, directoryRoleAssignmentsOutput, error) {
		q := url.Values{}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", roleAssignFields)
		if uk := strings.TrimSpace(in.UserKey); uk != "" {
			q.Set("userKey", uk)
		}
		if r := strings.TrimSpace(in.RoleID); r != "" {
			q.Set("roleId", r)
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/customer/"+myCustomer+"/roleassignments", q)
		if err != nil {
			return nil, directoryRoleAssignmentsOutput{}, toolError(err)
		}
		var env struct {
			Items         []DirectoryRoleAssignment `json:"items"`
			NextPageToken string                    `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, directoryRoleAssignmentsOutput{}, fmt.Errorf("decoding role assignments: %w", err)
		}
		out := directoryRoleAssignmentsOutput{Assignments: env.Items, Count: len(env.Items), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d role assignments", out.Count)), out, nil
	})
}
