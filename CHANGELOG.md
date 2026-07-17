# Changelog

All notable changes to this project are documented here. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); entries are grouped
by milestone.

## [Unreleased]

### Added

- **M0 — scaffold.** Go module and stdio MCP server on the official go-sdk,
  with a `health` tool reporting name/version/transport/mode, gate state, and
  GWS_* config presence (booleans only — never a value). Env-driven config
  (`internal/config`) with strict `true`-only gate parsing, a value-free
  `Presence` struct, and a `Redact` helper; `--allow-writes` / `--allow-sends`
  flags OR with their env vars (contract only — no gated tools yet). Stderr
  slog (stdout belongs to the protocol), CI (gofmt/vet/test), Apache-2.0
  license. Tests cover config parsing, gate independence, and a
  secret-never-leaks assertion over the full `health` result. Dependency:
  `github.com/modelcontextprotocol/go-sdk` v1.6.1 (the MCP protocol
  implementation; same version as the sibling entra-mcp-server).
