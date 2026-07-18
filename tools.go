package main

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Result-size bounds shared by the list/search tools.
const (
	defaultLimit = 25
	maxLimit     = 100
)

// clampLimit bounds a caller-supplied result cap to [1, maxLimit], defaulting a
// non-positive value to defaultLimit.
func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultLimit
	case n > maxLimit:
		return maxLimit
	default:
		return n
	}
}

// --- MCP tool annotations ---
//
// Annotations are the hints a client — or a policy layer sitting in front of one
// — reads to judge a tool before calling it. The spec's defaults are
// deliberately pessimistic: a tool that declares nothing is assumed read-write,
// destructive, non-idempotent, and open-world. Every tool here therefore
// declares its actual shape, so a caller can tell a mailbox read from a user
// suspension without pattern-matching on names.
//
// They describe; they do not enforce. Enforcement is the write/send gates in
// write.go plus Google's own authorization, and a client is right to treat
// annotations from an untrusted server as unverified claims.
//
// One subtlety worth stating, because it looks like an omission: destructiveHint
// follows the spec's definition — deleting or overwriting, as opposed to adding.
// An action can therefore be irreversible without being "destructive": sending
// mail creates a new message and destroys nothing, so gmail_send is
// destructiveHint=false. Irreversibility is carried by the SEPARATE send gate,
// which every such tool names in its description.

// ptrTo returns a pointer to v. The annotation booleans are pointers because the
// spec distinguishes "declared false" from "not declared".
func ptrTo[T any](v T) *T { return &v }

// readAnnotations describes a tool that only reads Google data. Open-world: it
// reaches an external service whose contents this server does not control.
func readAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: ptrTo(true),
	}
}

// localAnnotations describes a tool that reads only this server's own state and
// makes no Google call — a closed world.
func localAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: ptrTo(false),
	}
}

// additiveAnnotations describes a mutation that creates new state — a draft, an
// event, a group member, an outgoing message. It is not idempotent: calling it
// again creates another one.
func additiveAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptrTo(false),
		OpenWorldHint:   ptrTo(true),
	}
}

// destructiveAnnotations describes a mutation that overwrites or removes
// existing state — patching a resource in place, cancelling an event, removing a
// member, suspending a user. Both shapes are idempotent: repeating the call with
// the same arguments converges on the same end state.
func destructiveAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		DestructiveHint: ptrTo(true),
		IdempotentHint:  true,
		OpenWorldHint:   ptrTo(true),
	}
}

// truncateUTF8 caps b at limit bytes without splitting a multi-byte rune, and
// reports whether it cut. Slicing on a raw byte offset can leave a partial rune
// that renders as a replacement character, so any trailing fragment (at most
// three bytes, since a rune is at most four) is dropped.
func truncateUTF8(b []byte, limit int) ([]byte, bool) {
	if len(b) <= limit {
		return b, false
	}
	cut := b[:limit]
	for i := 0; i < 3 && len(cut) > 0; i++ {
		// DecodeLastRune reports (RuneError, 1) for a stray byte, but (RuneError, 3)
		// for a genuine U+FFFD — the size distinguishes them.
		if r, size := utf8.DecodeLastRune(cut); r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1]
			continue
		}
		break
	}
	return cut, true
}

// textualMIME reports whether a MIME type names content that is meaningful as
// text. Anything else — PDFs, images, archives, Office binaries — would reach the
// caller as a wall of replacement characters, so the content tools refuse it
// instead of filling a model's context with noise.
func textualMIME(mimeType string) bool {
	m := strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.IndexByte(m, ';'); i >= 0 { // drop parameters: "text/plain; charset=utf-8"
		m = strings.TrimSpace(m[:i])
	}
	if strings.HasPrefix(m, "text/") {
		return true
	}
	// Structured-syntax suffixes (RFC 6839): application/ld+json, atom+xml, …
	if strings.HasSuffix(m, "+json") || strings.HasSuffix(m, "+xml") || strings.HasSuffix(m, "+yaml") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml",
		"application/javascript", "application/x-javascript", "application/ecmascript",
		"application/typescript", "application/sql", "application/graphql",
		"application/x-sh", "application/x-shellscript", "application/rtf",
		"application/csv", "application/toml", "application/x-ndjson":
		return true
	}
	return false
}

// jsonString marshals v to a compact JSON string.
func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// toolError surfaces a Google API failure to the caller: when the client returns
// a *gapi.Error, its human-readable Message becomes the tool error; any other
// error is passed through unchanged. The bearer token and request body are never
// part of a *gapi.Error, so nothing sensitive leaks.
func toolError(err error) error {
	var ge *gapi.Error
	if errors.As(err, &ge) && ge.Message != "" {
		return errors.New(ge.Message)
	}
	return err
}
