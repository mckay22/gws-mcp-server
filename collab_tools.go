package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCollabTools installs the powerful-delegated collaboration tools: People
// contact search, Chat (spaces/messages read + send), Meet conference records,
// and a Drive shared-with-me shortcut. Chat and Meet are Workspace-only and error
// cleanly on consumer accounts. Sending a Chat message is send-gated.
func registerCollabTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerPeopleSearch(server, gc)
	registerChatListSpaces(server, gc)
	registerChatListMessages(server, gc)
	registerChatSendMessage(server, gc, allowWrites, allowSends)
	registerMeetConferenceRecords(server, gc)
	registerDriveSharedWithMe(server, gc)
}

// --- people_search_contacts ---

type peopleSearchInput struct {
	Query    string `json:"query" jsonschema:"free-text query over the user's contacts (name, email, phone) (required)"`
	PageSize int    `json:"pageSize,omitempty" jsonschema:"max results 1-30 (default 10)"`
}

// Contact is a compact People API contact result.
type Contact struct {
	ResourceName string   `json:"resourceName,omitempty"`
	DisplayName  string   `json:"displayName,omitempty"`
	Emails       []string `json:"emails,omitempty"`
	Phones       []string `json:"phones,omitempty"`
}

type peopleSearchOutput struct {
	Contacts []Contact `json:"contacts"`
	Count    int       `json:"count"`
}

func registerPeopleSearch(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "people_search_contacts",
		Annotations: readAnnotations(),
		Title:       "Search contacts",
		Description: "Search the signed-in user's personal contacts by name, email, or phone (People API). Returns compact contact cards.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in peopleSearchInput) (*mcp.CallToolResult, peopleSearchOutput, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, peopleSearchOutput{}, fmt.Errorf("query is required")
		}
		size := in.PageSize
		if size <= 0 {
			size = 10
		}
		if size > 30 {
			size = 30
		}
		q := url.Values{
			"query":    {in.Query},
			"pageSize": {strconv.Itoa(size)},
			"readMask": {"names,emailAddresses,phoneNumbers"},
		}
		raw, err := gc.Get(ctx, gapi.BasePeople, "/people:searchContacts", q)
		if err != nil {
			return nil, peopleSearchOutput{}, toolError(err)
		}
		var env struct {
			Results []struct {
				Person struct {
					ResourceName string `json:"resourceName"`
					Names        []struct {
						DisplayName string `json:"displayName"`
					} `json:"names"`
					EmailAddresses []struct {
						Value string `json:"value"`
					} `json:"emailAddresses"`
					PhoneNumbers []struct {
						Value string `json:"value"`
					} `json:"phoneNumbers"`
				} `json:"person"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, peopleSearchOutput{}, fmt.Errorf("decoding contacts: %w", err)
		}
		contacts := make([]Contact, 0, len(env.Results))
		for _, r := range env.Results {
			c := Contact{ResourceName: r.Person.ResourceName}
			if len(r.Person.Names) > 0 {
				c.DisplayName = r.Person.Names[0].DisplayName
			}
			for _, e := range r.Person.EmailAddresses {
				c.Emails = append(c.Emails, e.Value)
			}
			for _, p := range r.Person.PhoneNumbers {
				c.Phones = append(c.Phones, p.Value)
			}
			contacts = append(contacts, c)
		}
		out := peopleSearchOutput{Contacts: contacts, Count: len(contacts)}
		return text(fmt.Sprintf("%d contacts", out.Count)), out, nil
	})
}

// --- chat_list_spaces ---

type chatListSpacesInput struct {
	PageToken string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// ChatSpace is a compact Google Chat space.
type ChatSpace struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Type        string `json:"spaceType,omitempty"`
}

type chatListSpacesOutput struct {
	Spaces        []ChatSpace `json:"spaces"`
	Count         int         `json:"count"`
	NextPageToken string      `json:"nextPageToken,omitempty"`
}

func registerChatListSpaces(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "chat_list_spaces",
		Annotations: readAnnotations(),
		Title:       "List Chat spaces",
		Description: "List the Google Chat spaces the signed-in user is a member of (Workspace-only). Returns space ids (names) for use with chat_list_messages/chat_send_message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chatListSpacesInput) (*mcp.CallToolResult, chatListSpacesOutput, error) {
		q := url.Values{"pageSize": {"100"}}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseChat, "/spaces", q)
		if err != nil {
			return nil, chatListSpacesOutput{}, toolError(err)
		}
		var env struct {
			Spaces        []ChatSpace `json:"spaces"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, chatListSpacesOutput{}, fmt.Errorf("decoding spaces: %w", err)
		}
		out := chatListSpacesOutput{Spaces: env.Spaces, Count: len(env.Spaces), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d spaces", out.Count)), out, nil
	})
}

// --- chat_list_messages ---

type chatListMessagesInput struct {
	Space     string `json:"space" jsonschema:"the space id/name, e.g. 'spaces/AAAA' (required)"`
	PageSize  int    `json:"pageSize,omitempty" jsonschema:"max messages 1-100 (default 25)"`
	PageToken string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// ChatMessage is a compact Google Chat message.
type ChatMessage struct {
	Name       string `json:"name"`
	Text       string `json:"text,omitempty"`
	CreateTime string `json:"createTime,omitempty"`
	Sender     struct {
		Name string `json:"name"`
	} `json:"sender,omitempty"`
}

type chatListMessagesOutput struct {
	Messages      []ChatMessage `json:"messages"`
	Count         int           `json:"count"`
	NextPageToken string        `json:"nextPageToken,omitempty"`
}

func registerChatListMessages(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "chat_list_messages",
		Annotations: readAnnotations(),
		Title:       "List Chat messages",
		Description: "List messages in a Google Chat space (Workspace-only), most recent first. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chatListMessagesInput) (*mcp.CallToolResult, chatListMessagesOutput, error) {
		space := strings.TrimSpace(in.Space)
		if space == "" {
			return nil, chatListMessagesOutput{}, fmt.Errorf("space is required")
		}
		space = strings.TrimPrefix(space, "spaces/")
		size := clampLimit(in.PageSize)
		// Chat defaults to createTime ASC — the OLDEST messages in the space. A
		// caller asking "what's happening here" wants the newest, and with a 25-item
		// page the default would hand back the start of the space's history.
		q := url.Values{
			"pageSize": {strconv.Itoa(size)},
			"orderBy":  {"createTime desc"},
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseChat, "/spaces/"+url.PathEscape(space)+"/messages", q)
		if err != nil {
			return nil, chatListMessagesOutput{}, toolError(err)
		}
		var env struct {
			Messages      []ChatMessage `json:"messages"`
			NextPageToken string        `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, chatListMessagesOutput{}, fmt.Errorf("decoding messages: %w", err)
		}
		out := chatListMessagesOutput{Messages: env.Messages, Count: len(env.Messages), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d messages", out.Count)), out, nil
	})
}

// --- chat_send_message (send gate) ---

type chatSendInput struct {
	Space string `json:"space" jsonschema:"the space id/name to post to, e.g. 'spaces/AAAA' (required)"`
	Text  string `json:"text" jsonschema:"the message text (required)"`
}

func registerChatSendMessage(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "chat_send_message",
		Annotations: additiveAnnotations(),
		Title:       "Send a Chat message",
		Description: "Post a message to a Google Chat space as the signed-in user (Workspace-only). Sending is irreversible, so it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview of the exact message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in chatSendInput) (*mcp.CallToolResult, writeOutput, error) {
		space := strings.TrimSpace(in.Space)
		if space == "" || strings.TrimSpace(in.Text) == "" {
			return nil, writeOutput{}, fmt.Errorf("space and text are required")
		}
		space = strings.TrimPrefix(space, "spaces/")
		plan := writePlan{
			Summary: fmt.Sprintf("send Chat message to spaces/%s", space),
			Gate:    gateSends,
			Method:  "POST",
			Base:    gapi.BaseChat,
			Path:    "/spaces/" + url.PathEscape(space) + "/messages",
			Body:    map[string]any{"text": in.Text},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- meet_conference_records ---

type meetRecordsInput struct {
	Filter    string `json:"filter,omitempty" jsonschema:"optional Meet filter, e.g. \"space.meeting_code=abc-mnop-xyz\" or a time range"`
	PageSize  int    `json:"pageSize,omitempty" jsonschema:"max records 1-50 (default 10)"`
	PageToken string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// ConferenceRecord is a compact Meet conference record.
type ConferenceRecord struct {
	Name      string `json:"name"`
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
	Space     string `json:"space,omitempty"`
}

type meetRecordsOutput struct {
	Records       []ConferenceRecord `json:"records"`
	Count         int                `json:"count"`
	NextPageToken string             `json:"nextPageToken,omitempty"`
}

func registerMeetConferenceRecords(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "meet_conference_records",
		Annotations: readAnnotations(),
		Title:       "List Meet conference records",
		Description: "List Google Meet conference records the signed-in user has access to (Workspace, edition-gated — errors cleanly if unavailable). Each record's id leads to its recordings/transcripts. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in meetRecordsInput) (*mcp.CallToolResult, meetRecordsOutput, error) {
		size := in.PageSize
		if size <= 0 {
			size = 10
		}
		if size > 50 {
			size = 50
		}
		q := url.Values{"pageSize": {strconv.Itoa(size)}}
		if s := strings.TrimSpace(in.Filter); s != "" {
			q.Set("filter", s)
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseMeet, "/conferenceRecords", q)
		if err != nil {
			return nil, meetRecordsOutput{}, toolError(err)
		}
		var env struct {
			ConferenceRecords []ConferenceRecord `json:"conferenceRecords"`
			NextPageToken     string             `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, meetRecordsOutput{}, fmt.Errorf("decoding conference records: %w", err)
		}
		out := meetRecordsOutput{Records: env.ConferenceRecords, Count: len(env.ConferenceRecords), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d conference records", out.Count)), out, nil
	})
}

// --- drive_shared_with_me ---

type sharedWithMeInput struct {
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

func registerDriveSharedWithMe(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "drive_shared_with_me",
		Annotations: readAnnotations(),
		Title:       "List files shared with me",
		Description: "List Drive files that others have shared with the signed-in user (most recently modified first). Returns file metadata; page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sharedWithMeInput) (*mcp.CallToolResult, listFilesOutput, error) {
		q := url.Values{}
		q.Set("q", "sharedWithMe = true and trashed = false")
		q.Set("orderBy", "modifiedTime desc")
		q.Set("pageSize", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", fileListFields)
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseDrive, "/files", q)
		if err != nil {
			return nil, listFilesOutput{}, toolError(err)
		}
		var env struct {
			Files         []DriveFile `json:"files"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listFilesOutput{}, fmt.Errorf("decoding files: %w", err)
		}
		out := listFilesOutput{Files: env.Files, Count: len(env.Files), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d files shared with me", out.Count)), out, nil
	})
}
