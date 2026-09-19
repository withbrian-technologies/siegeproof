# Changelog

## Unreleased

- Added the Phase 0/1 foundation and Go module.
- Added safe `version`, `doctor`, and `config validate` commands.
- Added documented, fail-closed YAML configuration validation.
- Added safe bounded MCP stdio discovery (`discover --config PATH`), including
  initialize and tools/resources/prompts listing with JSON-RPC framing.
- Added deterministic versioned discovery reports
  (`siegeproof.discovery/v1`), atomic report writes, `--report`, JSON
  envelopes, and offline `report validate --path PATH`.
- HTTP/SSE, tool calls, mutation, fuzzing, exploitation, scoring, and report
  generation beyond discovery are not implemented yet.
