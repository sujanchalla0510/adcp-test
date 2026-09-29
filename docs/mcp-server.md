# MCP server (dogfood)

`adcp-test mcp` serves the test suite itself as MCP tools over stdio
(JSON-RPC 2.0, newline-delimited). An AI agent — or any MCP client — can
drive conformance, scenarios, the signing debugger, and lifecycle checks
without shelling out to the CLI.

## Connect

Claude Code (`~/.claude.json` or project `.mcp.json`):

```json
{
  "mcpServers": {
    "adcp-test": {
      "command": "adcp-test",
      "args": ["mcp"]
    }
  }
}
```

Any client that speaks MCP over stdio works: spawn `adcp-test mcp`, send
one JSON-RPC message per line on stdin, read responses on stdout. Logs go
to stderr, so stdout stays a clean RPC channel.

Protocol: `initialize` → `tools/list` → `tools/call`. Also answers `ping`
and `notifications/initialized`.

## Tools

| Tool              | Arguments                                          | Returns                                                        |
|-------------------|----------------------------------------------------|----------------------------------------------------------------|
| `run_conformance` | `target` (required), `bearer_token`                | Pass/fail summary + full 24-check conformance report           |
| `list_scenarios`  | —                                                  | Built-in scenario packs (id, name, scenarios)                  |
| `run_scenario`    | `pack` (required), `target` (required), `bearer_token`, `chaos` | Pass/fail summary + full scenario report |
| `debug_signature` | `request` (`{method, url, headers, body}`) (required), `key_pem` (required), `expected_base` | Verdict + signature-base diff |
| `lifecycle_check` | `target` (required), `bearer_token`                | Pass/fail summary + full lifecycle report                      |

Every tool returns an envelope with a human-readable `summary`, a boolean
`passed`, and the full machine-readable `report` — the same report the CLI
prints, so agents can drill into per-check detail.

Example session:

```
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}
← {"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"adcp-test","version":"0.3.0"},"capabilities":{"tools":{}}}}
→ {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_conformance","arguments":{"target":"http://127.0.0.1:8089"}}}
← {"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"summary\": \"conformance: 17 passed, ...\", ...}"}]}}
```

## Notes

- Tool calls are bounded (conformance 3 min, scenarios 10 min, lifecycle
  5 min per call).
- Chaos mode via `run_scenario` only runs against localhost targets — the
  same guard as the CLI.
- **Key material:** `bearer_token` and `key_pem` are held in memory only
  and never written to disk by adcp-test, but your MCP host may log tool
  calls. Prefer short-lived test credentials, and use the signing debugger
  against synthetic requests.
