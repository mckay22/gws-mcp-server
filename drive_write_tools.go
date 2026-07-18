package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	"github.com/mckay22/gws-mcp-server/internal/config"
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerDriveWriteTools installs the M3 gated Drive mutation tools. Uploading a
// file is a reversible write (allowWrites); creating a permission shares a file —
// egress, like the sibling's share-link rule — so it rides the SEPARATE send gate
// (allowSends).
func registerDriveWriteTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerUploadFile(server, gc, allowWrites, allowSends)
	registerShareFile(server, gc, allowWrites, allowSends)
}

// newMultipartBoundary returns a fresh random delimiter for one upload.
//
// A multipart body is parsed by delimiter, not by length, so a boundary that
// appears inside the content splits the stream into bogus parts and corrupts the
// upload. The content here is entirely caller-supplied, so the boundary must not
// be a constant a caller can reproduce: 128 random bits makes an accidental — or
// deliberate — collision infeasible.
func newMultipartBoundary() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating upload boundary: %w", err)
	}
	return "gws-mcp-" + hex.EncodeToString(b), nil
}

// buildMultipartUpload assembles a Drive multipart/related upload body: a JSON
// metadata part followed by the file content part.
func buildMultipartUpload(boundary, metadataJSON, contentType, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: application/json; charset=UTF-8\r\n\r\n")
	b.WriteString(metadataJSON)
	fmt.Fprintf(&b, "\r\n--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: %s\r\n\r\n", contentType)
	b.WriteString(content)
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.String()
}

// --- upload_file (write gate) ---

type uploadFileInput struct {
	Name     string `json:"name" jsonschema:"the file name to create (required)"`
	Content  string `json:"content" jsonschema:"the text content of the file (required)"`
	MimeType string `json:"mimeType,omitempty" jsonschema:"MIME type of the content (default text/plain)"`
	ParentID string `json:"parentId,omitempty" jsonschema:"optional parent folder id; omit for My Drive root"`
}

func registerUploadFile(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "upload_file",
		Annotations: additiveAnnotations(),
		Title:       "Upload a Drive file",
		Description: "Create a new file in Drive with the given text content (multipart upload). Reversible, so it rides the write gate: without " + config.EnvAllowWrites + "=true it returns a dry-run preview of the metadata and content instead of uploading.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in uploadFileInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, writeOutput{}, fmt.Errorf("name is required")
		}
		mimeType := strings.TrimSpace(in.MimeType)
		if mimeType == "" {
			mimeType = "text/plain"
		}

		metadata := map[string]any{"name": in.Name}
		if p := strings.TrimSpace(in.ParentID); p != "" {
			metadata["parents"] = []string{p}
		}
		metadataJSON, err := jsonString(metadata)
		if err != nil {
			return nil, writeOutput{}, err
		}
		boundary, err := newMultipartBoundary()
		if err != nil {
			return nil, writeOutput{}, err
		}
		bodyStr := buildMultipartUpload(boundary, metadataJSON, mimeType, in.Content)

		plan := writePlan{
			Summary:        fmt.Sprintf("upload file %q (%s, %d bytes)", in.Name, mimeType, len(in.Content)),
			Gate:           gateWrites,
			Method:         "POST",
			Base:           gapi.BaseDriveUpload,
			Path:           "/files",
			Query:          url.Values{"uploadType": {"multipart"}, "fields": {"id,name,mimeType,webViewLink"}},
			Body:           map[string]any{"name": in.Name, "mimeType": mimeType, "parentId": in.ParentID, "bytes": len(in.Content)},
			RawBody:        []byte(bodyStr),
			RawContentType: "multipart/related; boundary=" + boundary,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// --- share_file (send gate: creating a permission is egress) ---

type shareFileInput struct {
	FileID       string `json:"fileId" jsonschema:"the id of the file/folder to share (required)"`
	Role         string `json:"role" jsonschema:"permission role: reader, commenter, or writer (required)"`
	Type         string `json:"type" jsonschema:"grantee type: user, group, domain, or anyone (required)"`
	EmailAddress string `json:"emailAddress,omitempty" jsonschema:"grantee email (required for type user/group)"`
	Domain       string `json:"domain,omitempty" jsonschema:"grantee domain (required for type domain)"`
	SendEmail    bool   `json:"sendNotificationEmail,omitempty" jsonschema:"email the grantee a notification (default false)"`
}

func registerShareFile(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "share_file",
		Annotations: additiveAnnotations(),
		Title:       "Share a Drive file (grant access)",
		Description: "Grant a permission on a Drive file or folder (POST /files/{id}/permissions) — this exposes the file to another principal (egress). Because it grants access outside the current owner, it is gated by the SEPARATE send gate: without " + config.EnvAllowSends + "=true it returns a dry-run preview of the exact grant instead of applying it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in shareFileInput) (*mcp.CallToolResult, writeOutput, error) {
		if strings.TrimSpace(in.FileID) == "" {
			return nil, writeOutput{}, fmt.Errorf("fileId is required")
		}
		role := strings.ToLower(strings.TrimSpace(in.Role))
		switch role {
		case "reader", "commenter", "writer":
		default:
			return nil, writeOutput{}, fmt.Errorf("role must be reader, commenter, or writer, got %q", in.Role)
		}
		gtype := strings.ToLower(strings.TrimSpace(in.Type))
		body := map[string]any{"role": role, "type": gtype}
		switch gtype {
		case "user", "group":
			if strings.TrimSpace(in.EmailAddress) == "" {
				return nil, writeOutput{}, fmt.Errorf("emailAddress is required for type %q", gtype)
			}
			body["emailAddress"] = in.EmailAddress
		case "domain":
			if strings.TrimSpace(in.Domain) == "" {
				return nil, writeOutput{}, fmt.Errorf("domain is required for type domain")
			}
			body["domain"] = in.Domain
		case "anyone":
			// no grantee identifier
		default:
			return nil, writeOutput{}, fmt.Errorf("type must be user, group, domain, or anyone, got %q", in.Type)
		}

		q := url.Values{"fields": {"id,role,type"}, "supportsAllDrives": {"true"}}
		q.Set("sendNotificationEmail", boolStr(in.SendEmail))

		grantee := in.EmailAddress
		if grantee == "" {
			grantee = in.Domain
		}
		if grantee == "" {
			grantee = gtype
		}
		plan := writePlan{
			Summary: fmt.Sprintf("share file %s: grant %s to %s (%s)", in.FileID, role, grantee, gtype),
			Gate:    gateSends,
			Method:  "POST",
			Base:    gapi.BaseDrive,
			Path:    "/files/" + url.PathEscape(in.FileID) + "/permissions",
			Query:   q,
			Body:    body,
		}
		return runWrite(ctx, gc, allowWrites, allowSends, plan)
	})
}

// boolStr renders a bool as the "true"/"false" query string Google expects.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
