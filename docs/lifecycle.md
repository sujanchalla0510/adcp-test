# Lifecycle checks

Walk one media buy through create → activate → pause → resume → cancel,
then verify the illegal cancelled → active transition is rejected with a
structured JSON-RPC error (`-32001`).

```sh
./adcp-test lifecycle --target https://seller.example/mcp [--bearer-token T]
```

JSON report on stdout; exit 0 when every check passes. A built-in
`lifecycle` scenario pack runs the same flow with per-step assertions.
API: `POST /api/lifecycle/run`.
