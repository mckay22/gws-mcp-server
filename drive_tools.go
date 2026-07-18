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

// registerDriveReadTools installs the M2 read-only Drive tools. Every tool acts
// as the signed-in user, so Google's Drive ACLs decide what the caller can see
// or download.
func registerDriveReadTools(server *mcp.Server, gc *gapi.Client) {
	registerListFiles(server, gc)
	registerGetFileContent(server, gc)
}

// maxFileContentBytes caps how many bytes of file/export content get_file_content
// returns, so a large document can't flood model context.
const maxFileContentBytes = 200 << 10 // 200 KiB

// fileListFields projects Drive file metadata down to what the tool surfaces.
const fileListFields = "files(id,name,mimeType,modifiedTime,size,owners(emailAddress,displayName),webViewLink,parents,driveId,shared,trashed),nextPageToken,incompleteSearch"

// fileMetaFields is the metadata get_file_content needs to decide export vs
// direct download.
const fileMetaFields = "id,name,mimeType,size"

// googleAppsExports maps the Google-native editor MIME types to the export MIME
// type get_file_content requests (always a plain-text form to keep context
// small). Files not in this map are downloaded directly via alt=media.
var googleAppsExports = map[string]string{
	"application/vnd.google-apps.document":     "text/plain",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

// --- list_files ---

type listFilesInput struct {
	Query       string `json:"query,omitempty" jsonschema:"Drive search query (e.g. \"name contains 'report'\", \"mimeType='application/pdf'\", \"modifiedTime > '2026-01-01T00:00:00'\"); omit to list recent files"`
	OrderBy     string `json:"orderBy,omitempty" jsonschema:"sort order (default 'modifiedTime desc'); e.g. 'name', 'modifiedTime', 'quotaBytesUsed desc'"`
	MaxResults  int    `json:"maxResults,omitempty" jsonschema:"page size 1-100 (default 25)"`
	PageToken   string `json:"pageToken,omitempty" jsonschema:"continuation token from a previous call's nextPageToken"`
	IncludeAll  bool   `json:"includeSharedDrives,omitempty" jsonschema:"also include items from shared drives (default false = My Drive only)"`
	TrashedOnly bool   `json:"trashedOnly,omitempty" jsonschema:"list only trashed files (default false excludes trash)"`
}

// DriveOwner is a trimmed file-owner reference.
type DriveOwner struct {
	EmailAddress string `json:"emailAddress,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
}

// DriveFile is the compact file metadata returned by list_files.
type DriveFile struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	MimeType     string       `json:"mimeType,omitempty"`
	ModifiedTime string       `json:"modifiedTime,omitempty"`
	Size         string       `json:"size,omitempty"`
	Owners       []DriveOwner `json:"owners,omitempty"`
	WebViewLink  string       `json:"webViewLink,omitempty"`
	Parents      []string     `json:"parents,omitempty"`
	DriveID      string       `json:"driveId,omitempty"`
	Shared       bool         `json:"shared,omitempty"`
	Trashed      bool         `json:"trashed,omitempty"`
}

type listFilesOutput struct {
	Files            []DriveFile `json:"files"`
	Count            int         `json:"count"`
	NextPageToken    string      `json:"nextPageToken,omitempty"`
	IncompleteSearch bool        `json:"incompleteSearch,omitempty"`
}

func registerListFiles(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_files",
		Annotations: readAnnotations(),
		Title:       "List/search Drive files",
		Description: "List or search the signed-in user's Drive files. With no query, returns recent files (newest first). With a query, uses Drive's search syntax. Returns one page of metadata (no content); page with nextPageToken. Optionally spans shared drives.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listFilesInput) (*mcp.CallToolResult, listFilesOutput, error) {
		q := url.Values{}
		q.Set("pageSize", strconv.Itoa(clampLimit(in.MaxResults)))
		q.Set("fields", fileListFields)
		orderBy := strings.TrimSpace(in.OrderBy)
		if orderBy == "" {
			orderBy = "modifiedTime desc"
		}
		q.Set("orderBy", orderBy)

		// Combine the caller's query with the trashed filter so the default
		// listing hides trash.
		clauses := make([]string, 0, 2)
		if s := strings.TrimSpace(in.Query); s != "" {
			clauses = append(clauses, "("+s+")")
		}
		if in.TrashedOnly {
			clauses = append(clauses, "trashed = true")
		} else {
			clauses = append(clauses, "trashed = false")
		}
		q.Set("q", strings.Join(clauses, " and "))

		if in.IncludeAll {
			q.Set("includeItemsFromAllDrives", "true")
			q.Set("supportsAllDrives", "true")
			q.Set("corpora", "allDrives")
		}
		if in.PageToken != "" {
			q.Set("pageToken", in.PageToken)
		}

		raw, err := gc.Get(ctx, gapi.BaseDrive, "/files", q)
		if err != nil {
			return nil, listFilesOutput{}, toolError(err)
		}
		var env struct {
			Files            []DriveFile `json:"files"`
			NextPageToken    string      `json:"nextPageToken"`
			IncompleteSearch bool        `json:"incompleteSearch"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, listFilesOutput{}, fmt.Errorf("decoding files: %w", err)
		}
		out := listFilesOutput{
			Files:            env.Files,
			Count:            len(env.Files),
			NextPageToken:    env.NextPageToken,
			IncompleteSearch: env.IncompleteSearch,
		}
		return text(fmt.Sprintf("%d files", out.Count)), out, nil
	})
}

// --- get_file_content ---

type getFileContentInput struct {
	FileID string `json:"fileId" jsonschema:"the Drive file id from list_files"`
}

type getFileContentOutput struct {
	FileID    string `json:"fileId"`
	Name      string `json:"name,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	Exported  bool   `json:"exported" jsonschema:"true when a Google-native file was exported to text/csv"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

func registerGetFileContent(server *mcp.Server, gc *gapi.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_file_content",
		Annotations: readAnnotations(),
		Title:       "Get Drive file content",
		Description: "Fetch a file's text content by id. Google Docs/Sheets/Slides are exported (to plain text / CSV); other files are downloaded directly. Binary files without a text form are rejected. Content is capped at 200 KiB (truncated flag set when longer).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getFileContentInput) (*mcp.CallToolResult, getFileContentOutput, error) {
		if strings.TrimSpace(in.FileID) == "" {
			return nil, getFileContentOutput{}, fmt.Errorf("fileId is required")
		}
		fileID := strings.TrimSpace(in.FileID)

		// First fetch metadata to decide export vs direct download.
		metaRaw, err := gc.Get(ctx, gapi.BaseDrive, "/files/"+url.PathEscape(fileID), url.Values{
			"fields":            {fileMetaFields},
			"supportsAllDrives": {"true"},
		})
		if err != nil {
			return nil, getFileContentOutput{}, toolError(err)
		}
		var meta struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
		}
		if err := json.Unmarshal(metaRaw, &meta); err != nil {
			return nil, getFileContentOutput{}, fmt.Errorf("decoding file metadata: %w", err)
		}

		out := getFileContentOutput{FileID: meta.ID, Name: meta.Name, MimeType: meta.MimeType}

		var body []byte
		if exportType, isGoogleApp := googleAppsExports[meta.MimeType]; isGoogleApp {
			body, _, err = gc.GetRaw(ctx, gapi.BaseDrive, "/files/"+url.PathEscape(fileID)+"/export", url.Values{"mimeType": {exportType}})
			out.Exported = true
		} else if strings.HasPrefix(meta.MimeType, "application/vnd.google-apps.") {
			// A Google-native type with no text export (Forms, Drawings, …).
			return nil, getFileContentOutput{}, fmt.Errorf("file %q (%s) has no text export available", meta.Name, meta.MimeType)
		} else if !textualMIME(meta.MimeType) {
			// Refuse before downloading: this tool returns text, and a PDF or image
			// would otherwise be handed back as a wall of replacement characters
			// that costs context and tells the caller nothing.
			return nil, getFileContentOutput{}, fmt.Errorf(
				"file %q is %s, which has no text form — get_file_content returns text only", meta.Name, meta.MimeType)
		} else {
			body, _, err = gc.GetRaw(ctx, gapi.BaseDrive, "/files/"+url.PathEscape(fileID), url.Values{
				"alt":               {"media"},
				"supportsAllDrives": {"true"},
			})
		}
		if err != nil {
			return nil, getFileContentOutput{}, toolError(err)
		}

		body, truncated := truncateUTF8(body, maxFileContentBytes)
		out.Truncated = truncated
		out.Content = string(body)
		return text(fmt.Sprintf("%s (%s), %d bytes", out.Name, out.MimeType, len(out.Content))), out, nil
	})
}
