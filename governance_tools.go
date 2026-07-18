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
//
// events.parameters is included deliberately: without it a row says "a download
// happened" or "a setting changed" but never which document or which setting,
// which is most of what an audit question is actually asking. Parameter values
// arrive in whichever typed field fits (value/multiValue/boolValue/intValue), so
// all four are projected.
const auditActivityFields = "items(id(time,uniqueQualifier,applicationName),actor(email,profileId),ipAddress," +
	"events(type,name,parameters(name,value,multiValue,boolValue,intValue))),nextPageToken"

// --- admin_list_audit_activities ---

type auditActivitiesInput struct {
	Application string `json:"application" jsonschema:"the audited application: login, admin, drive, token, groups, calendar, mobile, user_accounts, … (required)"`
	UserKey     string `json:"userKey,omitempty" jsonschema:"limit to a user's activity by email/id, or 'all' (default all)"`
	EventName   string `json:"eventName,omitempty" jsonschema:"filter to a single event name (application-specific)"`
	StartTime   string `json:"startTime,omitempty" jsonschema:"RFC3339 lower bound on event time"`
	EndTime     string `json:"endTime,omitempty" jsonschema:"RFC3339 upper bound on event time"`
	MaxResults  int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken   string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// EventParameter is one name/value detail of an audited event — the document
// touched, the setting changed, the user affected. Google returns the value in
// whichever typed field fits the parameter, so Value carries whichever was set,
// normalized to a string (or a comma-joined list for a multi-value parameter).
type EventParameter struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
}

// EventRef is a single audited event within an activity.
type EventRef struct {
	Type       string           `json:"type,omitempty"`
	Name       string           `json:"name,omitempty"`
	Parameters []EventParameter `json:"parameters,omitempty"`
}

// UnmarshalJSON flattens Google's typed parameter shape
// ({"name":"doc_id","value":"1a2b"} / {"boolValue":true} / {"multiValue":[…]})
// into plain name/value pairs, so a caller reads one field instead of four.
func (e *EventRef) UnmarshalJSON(b []byte) error {
	var raw struct {
		Type       string `json:"type"`
		Name       string `json:"name"`
		Parameters []struct {
			Name       string   `json:"name"`
			Value      *string  `json:"value"`
			MultiValue []string `json:"multiValue"`
			BoolValue  *bool    `json:"boolValue"`
			IntValue   *string  `json:"intValue"` // Google sends int64 as a string
		} `json:"parameters"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	e.Type, e.Name = raw.Type, raw.Name
	e.Parameters = nil
	for _, p := range raw.Parameters {
		var v string
		switch {
		case p.Value != nil:
			v = *p.Value
		case len(p.MultiValue) > 0:
			v = strings.Join(p.MultiValue, ", ")
		case p.BoolValue != nil:
			v = strconv.FormatBool(*p.BoolValue)
		case p.IntValue != nil:
			v = *p.IntValue
		default:
			continue // a parameter with no value carries nothing worth reporting
		}
		e.Parameters = append(e.Parameters, EventParameter{Name: p.Name, Value: v})
	}
	return nil
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
		Name:        "admin_list_audit_activities",
		Annotations: readAnnotations(),
		InputSchema: enumSchema[auditActivitiesInput](map[string][]string{
			"application": {
				"access_transparency", "admin", "calendar", "chat", "drive", "gcp",
				"groups", "groups_enterprise", "login", "meet", "mobile", "rules",
				"saml", "token", "user_accounts",
			},
		}),
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

// --- admin_list_connected_apps (Directory tokens.list) ---

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
		Name:        "admin_list_connected_apps",
		Annotations: readAnnotations(),
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

// --- admin_list_license_assignments ---

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
		Name:        "admin_list_license_assignments",
		Annotations: readAnnotations(),
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
