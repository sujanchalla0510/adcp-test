# Conformance

Functional pass/fail of a seller endpoint: tool surface, input schemas,
auth behavior, error codes. 24 checks; headless `--ci` for CI pipelines
(also wrapped by the [GitHub Action](ci-action.md)).

## Run it

UI: **Conformance** → enter the seller's MCP endpoint URL → run, then
drill into failures.

CLI:

```sh
./adcp-test --ci --target https://seller.example/mcp [--bearer-token T]
```

JSON report on stdout; exit 0 when every check passes, 1 otherwise.

API: `POST /api/conformance/run` with `{"target_url": ...}`.

## Report

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

## Check families

- `tool-surface` — expected AdCP tool surface from `tools/list`.
- `schema:<tool>` — declared inputSchema well-formedness + expected
  required fields.
- `auth:*` — unsigned / malformed-signature mutating probes must be
  rejected; read-only behavior documented.
- `errors:*` — unknown tool and invalid args must return structured
  JSON-RPC errors, not HTML/plaintext.

The expected surface is the AdCP 3.1 v3-era task list
(`get_adcp_capabilities`, `get_products`, `list_creative_formats`,
`create_media_buy`, `update_media_buy`, `get_media_buys`,
`sync_creatives`, `sync_catalogs`, `list_creatives`,
`get_media_buy_delivery`, `provide_performance_feedback`, plus optional
sibling-protocol tools). Probes use deliberately invalid arguments, so no
conforming seller can mistake them for real mutations.
