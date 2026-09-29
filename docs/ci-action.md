# GitHub Action

Run the adcp-test conformance suite in CI against your seller agent's MCP
endpoint. The action wraps the headless `--ci` mode: it installs adcp-test,
runs the 24-check suite, uploads the JSON report as an artifact, and fails
the step when any check fails.

## Usage

```yaml
jobs:
  conformance:
    runs-on: ubuntu-latest
    steps:
      - uses: sujanchalla0510/adcp-test@v0.3.0
        with:
          target: https://seller.example/mcp
          # bearer-token: ${{ secrets.SELLER_BEARER_TOKEN }}
```

## Inputs

| Input          | Required | Default  | Description                                              |
|----------------|----------|----------|----------------------------------------------------------|
| `target`       | yes      | —        | Seller agent MCP endpoint URL                            |
| `version`      | no       | `latest` | Release to install (`v0.3.0`, `latest`); `checkout` builds from the action's own checkout (repo CI) |
| `bearer-token` | no       | —        | Bearer token for the target                              |
| `fail-on`      | no       | `fail`   | `fail` fails the step on any failed check; `never` keeps the step green (report still uploaded) |

## Outputs

| Output   | Description                              |
|----------|------------------------------------------|
| `passed` | `true` when every conformance check passed |
| `report` | Path to the JSON conformance report      |

The full report is also uploaded as the `adcp-conformance-report` artifact,
so you can download it from the workflow run or feed it to
`adcp-test snapshot save` / `adcp-test report` for regression tracking and
evidence packs.

## Example: gate a deploy on conformance

```yaml
- uses: sujanchalla0510/adcp-test@v0.3.0
  id: conformance
  with:
    target: ${{ vars.SELLER_MCP_URL }}
    bearer-token: ${{ secrets.SELLER_BEARER_TOKEN }}
    version: v0.3.0

- name: Comment the summary on the PR
  if: always()
  run: |
    python3 -c "
    import json
    r = json.load(open('${{ steps.conformance.outputs.report }}'))
    s = r['summary']
    print(f\"conformance: {s['passed']} passed, {s['failed']} failed, {s['skipped']} skipped of {s['total']}\")
    "
```

Pin the action to a release tag (`@v0.3.0`) rather than `@main` so spec
changes never surprise your pipeline. An end-to-end example that also
serves as this action's own CI lives in
[.github/workflows/adcp-conformance-example.yml](../.github/workflows/adcp-conformance-example.yml).
