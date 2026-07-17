package main

import (
	"github.com/mckay22/gws-mcp-server/internal/gapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPowerfulTools installs the powerful-delegated end-user tier (the
// --powerful / GWS_MCP_POWERFUL switch): Gmail settings, Tasks, People, Chat,
// Meet, and Drive shared-with-me. It is a registration switch only — every tool
// still honors the write/send gates. Both transports call it.
func registerPowerfulTools(server *mcp.Server, gc *gapi.Client, allowWrites, allowSends bool) {
	registerGmailSettingsTools(server, gc, allowWrites, allowSends)
	registerTasksTools(server, gc, allowWrites, allowSends)
	registerCollabTools(server, gc, allowWrites, allowSends)
}
