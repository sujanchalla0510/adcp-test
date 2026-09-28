# adcp-test

The integration test suite for AdCP. One local web app that answers **"does my AdCP implementation actually work?"** — for buyer agents and seller agents.

## Vision

Integrating AdCP means implementing ~13 MCP tools plus RFC 9421 request signing, schema compliance across fast-moving spec versions, and multi-step workflows (products → media buy → creatives → delivery). Today every implementer hand-rolls their own testing: curl scripts, SDK fixture mocks, manual sessions against the public test agent. adcp-test is the shared harness:

- **Conformance** — functional pass/fail of a seller endpoint: tool surface, schemas, auth behavior, error codes
- **Inspect** — session timeline debugger with inline schema checks
- **Record / replay** — cassettes for deterministic buyer tests
- **Mock builder** — config-driven mock seller services (request matching, response sequences, latency profiles, fault injection, lifecycle state machines), with record mode that generates configs from real traffic
- **Scenarios** — scripted end-to-end buying scenarios as runnable cards
- **Load** — concurrent sessions, live latency/throughput charts, CI thresholds
- **Plus** — signing debugger, lifecycle checks, fuzzing, chaos mode, webhook testing, regression snapshots, report exports

UI-first: a single Go binary serves the app on localhost. A headless `--ci` mode exists for CI pipelines only.

## Principles

- **Local-first.** No telemetry, no uploads, ever. Test traffic stays on your machine.
- **Synthetic fixtures only.** Never commit real integration data, credentials, or secrets.
- **Functional, not a linter.** Conformance is pass/fail on behavior, not style grading.

## Status

M1 scaffold (done) → M2 conformance core (done: MCP client, tool-surface / schema / auth / error checks, Conformance screen, `--ci --target`) → M3 inspect + record/replay → M4 mock builder → M5 scenarios + load → M6 polish + v0.1 launch → M7 GitHub Action, MCP server, spec-upgrade diffs.

Private repo until v0.1; public at launch.

## Run it

```sh
go run ./cmd/adcp-test            # serves the UI on http://127.0.0.1:18742
go run ./cmd/adcp-test --port 8080
go run ./cmd/adcp-test --ci --target https://seller.example/mcp  # headless conformance: JSON report on stdout, exit 0 when all checks pass, 1 otherwise
```

### Conformance

`--ci --target <url>` (or the Conformance screen, or `POST /api/conformance/run`
with `{"target_url": ...}`) runs the suite against a seller agent's MCP
endpoint and produces a machine-readable report:

```json
{
  "target_url": "https://seller.example/mcp",
  "started_at": "2026-09-28T...",
  "finished_at": "2026-09-28T...",
  "summary": { "total": 24, "passed": 24, "failed": 0, "skipped": 0 },
  "checks": [
    { "name": "tool-surface", "status": "pass", "detail": "...", "duration_ms": 12.3 },
    { "name": "schema:get_products", "status": "pass", "detail": "...", "duration_ms": 0.1 },
    { "name": "auth:unsigned-mutating-call-rejected", "status": "pass", "detail": "...", "duration_ms": 8.7 }
  ]
}
```

Check families: `tool-surface` (expected AdCP tool surface from `tools/list`),
`schema:<tool>` (declared inputSchema well-formedness + expected required
fields), `auth:*` (unsigned / malformed-signature mutating probes must be
rejected; read-only behavior documented), `errors:*` (unknown tool and
invalid args must return structured JSON-RPC errors, not HTML/plaintext).

## License

Apache-2.0. See [LICENSE](LICENSE).
