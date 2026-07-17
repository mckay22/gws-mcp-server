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

// registerGovernanceReadTools installs the M6 read-only governance tools: audit
// activities (Reports API), connected-app audit (Directory tokens), and license
// assignments. Like the directory reads they register only behind --admin and
// need the signed-in user to hold the matching admin privilege; Google enforces
// it. Some event types and license queries are edition-gated — those surface the
// Google error cleanly rather than pretending to succeed.
func registerGovernanceReadTools(server *mcp.Server, gc *gapi.Client) {
	registerAuditActivities(server, gc)
	registerConnectedApps(server, gc)
	registerLicenseAssignments(server, gc)
}

// auditActivityFields projects the Reports activity list to an audit overview.
const auditActivityFields = "items(id(time,uniqueQualifier,applicationName),actor(email,profileId),ipAddress,events(type,name)),nextPageToken"

// --- audit_activities ---

type auditActivitiesInput struct {
	Application string `json:"application" jsonschema:"the audited application: login, admin, drive, token, groups, calendar, mobile, user_accounts, … (required)"`
	UserKey     string `json:"userKey,omitempty" jsonschema:"limit to a user's activity by email/id, or 'all' (default all)"`
	EventName   string `json:"eventName,omitempty" jsonschema:"filter to a single event name (application-specific)"`
	StartTime   string `json:"startTime,omitempty" jsonschema:"RFC3339 lower bound on event time"`
	EndTime     string `json:"endTime,omitempty" jsonschema:"RFC3339 upper bound on event time"`
	MaxResults  int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken   string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// EventRef is a single audited event within an activity.
type EventRef struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
}

// AuditActivity is a compact audit-log entry.
type AuditActivity struct {
	Time       string     `json:"time,omitempty"`
	ActorEmail string     `json:"actorEmail,omitempty"`
	IPAddress  string     `json:"ipAddress,omitempty"`
	Events     []EventRef `json:"events,omitempty"`
}

type auditActivitiesOutput struct {
	Activities    []AuditActivity `json:"activities"`
	Count         int             `json:"count"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

func registerAuditActivities(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "audit_activities",
		Title:       "Query audit log activities",
		Description: "Query the Admin Reports audit log for an application (login, admin, drive, token, …): who did what, when, and from where. Some applications/event types are edition-gated and will error cleanly. Requires an admin caller with reporting privileges. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in auditActivitiesInput) (*mcp.CallToolResult, auditActivitiesOutput, error) {
		app := strings.TrimSpace(in.Application)
		if app == "" {
			return nil, auditActivitiesOutput{}, fmt.Errorf("application is required")
		}
		userKey := strings.TrimSpace(in.UserKey)
		if userKey == "" {
			userKey = "all"
		}
		if s := strings.TrimSpace(in.StartTime); s != "" && !validRFC3339(s) {
			return nil, auditActivitiesOutput{}, fmt.Errorf("startTime must be RFC3339")
		}
		if s := strings.TrimSpace(in.EndTime); s != "" && !validRFC3339(s) {
			return nil, auditActivitiesOutput{}, fmt.Errorf("endTime must be RFC3339")
		}

		q := url.Values{}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", auditActivityFields)
		if s := strings.TrimSpace(in.EventName); s != "" {
			q.Set("eventName", s)
		}
		if s := strings.TrimSpace(in.StartTime); s != "" {
			q.Set("startTime", s)
		}
		if s := strings.TrimSpace(in.EndTime); s != "" {
			q.Set("endTime", s)
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		path := "/activity/users/" + url.PathEscape(userKey) + "/applications/" + url.PathEscape(app)
		raw, err := gc.Get(ctx, gapi.BaseReports, path, q)
		if err != nil {
			return nil, auditActivitiesOutput{}, toolError(err)
		}

		var env struct {
			Items []struct {
				ID struct {
					Time string `json:"time"`
				} `json:"id"`
				Actor struct {
					Email string `json:"email"`
				} `json:"actor"`
				IPAddress string     `json:"ipAddress"`
				Events    []EventRef `json:"events"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, auditActivitiesOutput{}, fmt.Errorf("decoding activities: %w", err)
		}
		acts := make([]AuditActivity, 0, len(env.Items))
		for _, it := range env.Items {
			acts = append(acts, AuditActivity{
				Time:       it.ID.Time,
				ActorEmail: it.Actor.Email,
				IPAddress:  it.IPAddress,
				Events:     it.Events,
			})
		}
		out := auditActivitiesOutput{Activities: acts, Count: len(acts), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d %s activities", out.Count, app)), out, nil
	})
}

// --- user_connected_apps (Directory tokens.list) ---

type connectedAppsInput struct {
	UserKey string `json:"userKey" jsonschema:"the user's email or id whose issued OAuth tokens to list (required)"`
}

// ConnectedApp is a third-party app the user has granted an OAuth token to.
type ConnectedApp struct {
	ClientID    string   `json:"clientId"`
	DisplayText string   `json:"displayText,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	NativeApp   bool     `json:"nativeApp,omitempty"`
	Anonymous   bool     `json:"anonymous,omitempty"`
}

type connectedAppsOutput struct {
	Apps  []ConnectedApp `json:"apps"`
	Count int            `json:"count"`
}

func registerConnectedApps(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "user_connected_apps",
		Title:       "List a user's connected OAuth apps",
		Description: "List the third-party applications a user has granted OAuth access to (Directory tokens) — the connected-app / consent audit, with each app's granted scopes. Requires an admin caller with the user-security privilege.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in connectedAppsInput) (*mcp.CallToolResult, connectedAppsOutput, error) {
		if strings.TrimSpace(in.UserKey) == "" {
			return nil, connectedAppsOutput{}, fmt.Errorf("userKey is required")
		}
		q := url.Values{"fields": {"items(clientId,displayText,scopes,nativeApp,anonymous)"}}
		raw, err := gc.Get(ctx, gapi.BaseDirectory, "/users/"+url.PathEscape(in.UserKey)+"/tokens", q)
		if err != nil {
			return nil, connectedAppsOutput{}, toolError(err)
		}
		var env struct {
			Items []ConnectedApp `json:"items"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, connectedAppsOutput{}, fmt.Errorf("decoding tokens: %w", err)
		}
		out := connectedAppsOutput{Apps: env.Items, Count: len(env.Items)}
		return text(fmt.Sprintf("%d connected apps", out.Count)), out, nil
	})
}

// --- license_assignments ---

type licenseAssignmentsInput struct {
	ProductID  string `json:"productId" jsonschema:"the license product id, e.g. 'Google-Apps' for Workspace (required)"`
	CustomerID string `json:"customerId" jsonschema:"the account's customer id or primary domain (required; my_customer is not accepted by this API)"`
	SKUID      string `json:"skuId,omitempty" jsonschema:"limit to a single SKU id within the product"`
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// LicenseAssignment is one user's license within a product/SKU.
type LicenseAssignment struct {
	UserID    string `json:"userId"`
	ProductID string `json:"productId"`
	SKUID     string `json:"skuId"`
	SKUName   string `json:"skuName,omitempty"`
}

type licenseAssignmentsOutput struct {
	Assignments   []LicenseAssignment `json:"assignments"`
	Count         int                 `json:"count"`
	NextPageToken string              `json:"nextPageToken,omitempty"`
}

func registerLicenseAssignments(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "license_assignments",
		Title:       "List license assignments",
		Description: "List license assignments for a product (optionally a single SKU) across the account's users (Enterprise License Manager). Requires an admin caller; free/edition-limited tenants may return an error, which is surfaced. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in licenseAssignmentsInput) (*mcp.CallToolResult, licenseAssignmentsOutput, error) {
		productID := strings.TrimSpace(in.ProductID)
		customerID := strings.TrimSpace(in.CustomerID)
		if productID == "" || customerID == "" {
			return nil, licenseAssignmentsOutput{}, fmt.Errorf("productId and customerId are required")
		}
		q := url.Values{}
		q.Set("customerId", customerID)
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		// listForProduct, or listForProductAndSku when a SKU is given.
		path := "/product/" + url.PathEscape(productID) + "/users"
		if sku := strings.TrimSpace(in.SKUID); sku != "" {
			path = "/product/" + url.PathEscape(productID) + "/sku/" + url.PathEscape(sku) + "/users"
		}
		raw, err := gc.Get(ctx, gapi.BaseLicensing, path, q)
		if err != nil {
			return nil, licenseAssignmentsOutput{}, toolError(err)
		}
		var env struct {
			Items         []LicenseAssignment `json:"items"`
			NextPageToken string              `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, licenseAssignmentsOutput{}, fmt.Errorf("decoding license assignments: %w", err)
		}
		out := licenseAssignmentsOutput{Assignments: env.Items, Count: len(env.Items), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d license assignments", out.Count)), out, nil
	})
}
