# adcp-test

**Supports AdCP 3.1** — v3 task protocol + RFC 9421 request-signing auth model.

The integration test suite for AdCP. One local web app that answers
**"does my AdCP implementation actually work?"** — for seller agents
(conformance, scenarios, load, lifecycle, fuzzing) and for buyer agents
(point your buyer at a config-driven mock seller and make it misbehave).

## What tests what

- **Seller implementations** — conformance suite, scenario packs, load
  testing, lifecycle checks, fuzzing, spec-upgrade diffs, webhook
  capture.
- **Buyer implementations** — the mock server is your harness: fake the
  seller, inject faults and latency, record real sessions into cassettes
  for deterministic replay. The signing debugger verifies the signatures
  your buyer sends.
- **Either side** — session inspector, snapshots, evidence reports,
  MCP server, GitHub Action.

See [Testing buyer agents](docs/testing-buyer-agents.md) for the
buyer-side workflow end to end.

![Dashboard](docs/screenshots/dashboard.png)
![Conformance results](docs/screenshots/conformance.png)

## Install

Requirements: Go 1.27+.

```sh
git clone https://github.com/sujanchalla0510/adcp-test.git
cd adcp-test
go mod download
go build -o adcp-test ./cmd/adcp-test
./adcp-test            # serves the UI on http://127.0.0.1:18742
```

See [installation](docs/installation.md) for run options and headless modes.

## Quickstart

Point adcp-test at a seller agent's MCP endpoint and run the 24-check
conformance suite:

```sh
./adcp-test --ci --target https://seller.example/mcp
```

JSON report on stdout; exit 0 when every check passes, 1 otherwise. Or use
the web UI: open http://127.0.0.1:18742 → **Conformance** → enter the
endpoint → run.

No seller handy? The repo ships a synthetic mock seller for the full loop —
see the [5-minute quickstart](docs/quickstart.md).

## Docs

| Topic | What |
|---|---|
| [Quickstart](docs/quickstart.md) | 5-minute end-to-end walkthrough with screenshots |
| [Installation](docs/installation.md) | Requirements, build, run options |
| [Conformance](docs/conformance.md) | 24-check seller suite (UI, CLI, CI) |
| [Inspect](docs/inspect.md) | Session timeline, recording proxy, cassettes, replay |
| [Mock builder](docs/mock-builder.md) | Config-driven mock sellers, record mode — the buyer-testing harness |
| [Testing buyer agents](docs/testing-buyer-agents.md) | Buyer-side workflow: mock, fault injection, cassettes |
| [Scenarios](docs/scenarios.md) | Scripted buying flows, chaos mode |
| [Load testing](docs/load-testing.md) | Concurrent load engine, CI thresholds |
| [Signing debugger](docs/signing-debugger.md) | RFC 9421 signature-base reconstruction and diff |
| [Lifecycle](docs/lifecycle.md) | Media-buy state-machine checks |
| [Fuzzing](docs/fuzzing.md) | Malformed-payload robustness probes |
| [Webhooks](docs/webhooks.md) | Localhost webhook capture |
| [Snapshots](docs/snapshots.md) | Named report snapshots and regression diffs |
| [Reports](docs/reports.md) | HTML/PDF evidence packs |
| [GitHub Action](docs/ci-action.md) | Conformance in CI |
| [MCP server](docs/mcp-server.md) | Drive adcp-test from an AI agent |
| [Spec-upgrade diffs](docs/specdiff.md) | What breaks when the spec moves |

## Principles

- **Local-first.** No telemetry, no uploads, ever. Test traffic stays on
  your machine.
- **Synthetic fixtures only.** Never commit real integration data,
  credentials, or secrets.
- **Functional, not a linter.** Conformance is pass/fail on behavior, not
  style grading.

## Contributing

adcp-test is open source and community contributions are welcome. Bug
reports, feature ideas, and pull requests: please open an issue first for
larger changes so we can agree on the approach. Run `go test ./...`,
`go vet ./...`, and `gofmt` before submitting; CI must be green.

## License

Apache-2.0. See [LICENSE](LICENSE).
