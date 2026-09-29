# Inspect + record/replay

The Inspect screen records live MCP traffic and replays it
deterministically.

## Session timeline

Every proxied tool call appears as an OUT/IN step pair with
pretty-printed request/response payloads, timing, errors, and per-tool
argument-validation badges (same expectations as the conformance schema
checks).

## Recording proxy

`POST /api/record/start {"upstream_url": ...}` returns a proxy URL +
session ID; point a buyer at the proxy (header `X-Session-ID: <id>` pins
traffic to the session), then `POST /api/record/stop {"session_id": ...}`
saves a cassette under `./cassettes/`. Recording is secret-safe:
auth/signature headers and token-like fields are redacted at capture
time.

CLI:

```sh
./adcp-test record --upstream https://seller.example/mcp --out session.cassette.json
# point your buyer at the printed proxy URL; Ctrl-C writes the cassette
```

## Cassettes

Versioned JSON: `{version, target, recorded_at, exchanges: [{method,
tool, recorded_at, arguments, response, error?, duration_ms}]}`. Request
IDs are not stored (they vary per run).

## Replay server

Serves a cassette as a fake MCP endpoint, rewriting the recorded
response's `id` to the incoming request's. Lenient mode (default):
recorded arguments must be a subset of the incoming call's, falling back
to the first exchange for the same tool; unmatched calls get a structured
JSON-RPC error (`-32000`). `--strict` requires deep-equal arguments and
fails closed instead.

```sh
./adcp-test replay --cassette session.cassette.json --port 18743 [--strict]
# fake seller on http://127.0.0.1:18743; Ctrl-C stops
```
