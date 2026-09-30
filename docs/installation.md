# Installation

Requirements: **Go 1.27+**.

```sh
git clone https://github.com/sujanchalla0510/adcp-test.git
cd adcp-test
go mod download
go build -o adcp-test ./cmd/adcp-test
```

## Run modes

**Web UI** (the primary interface):

```sh
./adcp-test                 # serves the UI on http://127.0.0.1:18742
./adcp-test --port 8080     # override the port
```

Or without building: `go run ./cmd/adcp-test`.

**Headless** (for scripts and CI — every subcommand prints JSON to stdout
and exits 0 on success, 1 on failure):

```sh
./adcp-test --ci --target https://seller.example/mcp   # conformance suite
./adcp-test scenario --pack happy-path-media-buy --target https://seller.example/mcp
./adcp-test load --config load.yaml
./adcp-test signdebug --request req.json --key key.pem
./adcp-test lifecycle --target https://seller.example/mcp
./adcp-test fuzz --target http://127.0.0.1:8089
./adcp-test specdiff --from 3.1 --to 4.0-draft-expectations --target https://seller.example/mcp
./adcp-test mcp                                            # MCP server over stdio
```

`record`, `replay`, `mock`, `webhook-listen`, `snapshot`, and `report`
are covered in their respective docs. Run any subcommand with no flags
for its usage.

## Notes

- The UI binds localhost only. Headless load, fuzz, and chaos runs
  refuse non-localhost targets unless you pass `--allow-remote`.
- Everything runs on your machine: no telemetry, no uploads, no accounts.
