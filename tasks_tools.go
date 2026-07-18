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

// registerTasksTools installs the powerful-delegated Google Tasks tools: reading
// task lists and tasks, and (write-gated) creating and completing tasks.
func registerTasksTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerListTasklists(server, gc)
	registerListTasks(server, gc)
	registerTaskCreate(server, gc, allowWrites, allowSends)
	registerTaskComplete(server, gc, allowWrites, allowSends)
}

// fields projections keep the Tasks responses to what the tools surface
// (etag/selfLink/kind are fetched and discarded otherwise).
const (
	tasklistFields = "items(id,title,updated),nextPageToken"
	taskFields     = "items(id,title,notes,status,due),nextPageToken"
)

// --- tasks_list_tasklists ---

type listTasklistsInput struct {
	MaxResults int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken  string `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
}

// Tasklist is a compact task list.
type Tasklist struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Updated string `json:"updated,omitempty"`
}

type listTasklistsOutput struct {
	Tasklists     []Tasklist `json:"tasklists"`
	Count         int        `json:"count"`
	NextPageToken string     `json:"nextPageToken,omitempty"`
}

func registerListTasklists(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "tasks_list_tasklists",
		Annotations: readAnnotations(),
		Title:       "List task lists",
		Description: "List the signed-in user's Google Tasks lists, with their ids (use as tasklist in the other task tools). Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasklistsInput) (*mcp.CallToolResult, listTasklistsOutput, error) {
		q := url.Values{
			"maxResults": {strconv.Itoa(clampLimit(in.MaxResults))},
			"fields":     {tasklistFields},
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseTasks, "/users/@me/lists", q)
		if err != nil {
			return nil, listTasklistsOutput{}, toolError(err)
		}
		var env struct {
			Items         []Tasklist `json:"items"`
			NextPageToken string     `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listTasklistsOutput{}, fmt.Errorf("decoding task lists: %w", err)
		}
		out := listTasklistsOutput{Tasklists: env.Items, Count: len(env.Items), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d task lists", out.Count)), out, nil
	})
}

// --- tasks_list ---

type listTasksInput struct {
	Tasklist      string `json:"tasklist,omitempty" jsonschema:"the task list id (default '@default', the user's default list)"`
	ShowCompleted bool   `json:"showCompleted,omitempty" jsonschema:"include completed tasks (default false)"`
	MaxResults    int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken     string `json:"pageToken,omitempty" jsonschema:"continuation token"`
}

// Task is a compact Google task.
type Task struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Notes  string `json:"notes,omitempty"`
	Status string `json:"status,omitempty"`
	Due    string `json:"due,omitempty"`
}

type listTasksOutput struct {
	Tasks         []Task `json:"tasks"`
	Count         int    `json:"count"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

func registerListTasks(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "tasks_list",
		Annotations: readAnnotations(),
		Title:       "List tasks",
		Description: "List tasks in a task list (default the user's default list). Excludes completed tasks unless requested. Page with nextPageToken.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listTasksInput) (*mcp.CallToolResult, listTasksOutput, error) {
		tasklist := taskListOrDefault(in.Tasklist)
		q := url.Values{"fields": {taskFields}}
		q.Set("maxResults", strconv.Itoa(clampLimit(in.MaxResults)))
		if in.ShowCompleted {
			q.Set("showCompleted", "true")
			// Completing a task in the Google Tasks UI also marks it hidden, and
			// showCompleted alone still filters hidden tasks out — so without this
			// the "include completed" option returns almost none of them.
			q.Set("showHidden", "true")
		} else {
			q.Set("showCompleted", "false")
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}
		raw, err := gc.Get(ctx, gapi.BaseTasks, "/lists/"+url.PathEscape(tasklist)+"/tasks", q)
		if err != nil {
			return nil, listTasksOutput{}, toolError(err)
		}
		var env struct {
			Items         []Task `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listTasksOutput{}, fmt.Errorf("decoding tasks: %w", err)
		}
		out := listTasksOutput{Tasks: env.Items, Count: len(env.Items), NextPageToken: env.NextPageToken}
		return text(fmt.Sprintf("%d tasks", out.Count)), out, nil
	})
}

// --- tasks_create (write gate) ---

type taskCreateInput struct {
	Tasklist string `json:"tasklist,omitempty" jsonschema:"the task list id (default '@default')"`
	Title    string `json:"title" jsonschema:"the task title (required)"`
	Notes    string `json:"notes,omitempty" jsonschema:"optional task notes"`
	Due      string `json:"due,omitempty" jsonschema:"optional due date, RFC3339 (date portion is used)"`
}

func registerTaskCreate(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "tasks_create",
		Annotations: additiveAnnotations(),
		Title:       "Create a task",
		Description: "Create a task in a task list. Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskCreateInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.Title) == "" {
			return nil, writeOutput{}, fmt.Errorf("title is required")
		}
		if s := strings.TrimSpace(in.Due); s != "" && !validRFC3339(s) {
			return nil, writeOutput{}, fmt.Errorf("due must be RFC3339")
		}
		tasklist := taskListOrDefault(in.Tasklist)
		body := map[string]any{"title": in.Title}
		if s := strings.TrimSpace(in.Notes); s != "" {
			body["notes"] = s
		}
		if s := strings.TrimSpace(in.Due); s != "" {
			body["due"] = s
		}
		plan := writePlan{
			Summary: fmt.Sprintf("create task %q", in.Title),
			Gate:    gateWrites,
			Method:  "POST",
			Base:    gapi.BaseTasks,
			Path:    "/lists/" + url.PathEscape(tasklist) + "/tasks",
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- tasks_complete (write gate) ---

type taskCompleteInput struct {
	Tasklist string `json:"tasklist,omitempty" jsonschema:"the task list id (default '@default')"`
	TaskID   string `json:"taskId" jsonschema:"the task id to complete (required)"`
}

func registerTaskComplete(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "tasks_complete",
		Annotations: destructiveAnnotations(),
		Title:       "Complete a task",
		Description: "Mark a task as completed. Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskCompleteInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.TaskID) == "" {
			return nil, writeOutput{}, fmt.Errorf("taskId is required")
		}
		tasklist := taskListOrDefault(in.Tasklist)
		plan := writePlan{
			Summary: fmt.Sprintf("complete task %s", in.TaskID),
			Gate:    gateWrites,
			Method:  "PATCH",
			Base:    gapi.BaseTasks,
			Path:    "/lists/" + url.PathEscape(tasklist) + "/tasks/" + url.PathEscape(in.TaskID),
			Body:    map[string]any{"status": "completed"},
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// taskListOrDefault returns a trimmed task list id, defaulting to "@default".
func taskListOrDefault(id string) string {
	if s := strings.TrimSpace(id); s != "" {
		return s
	}
	return "@default"
}
