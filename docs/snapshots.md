# Snapshots

Save named JSON reports (conformance, scenario, load, lifecycle, fuzz)
under `./snapshots/` and diff two snapshots to see which checks flipped
pass → fail, fail → pass, new, or removed — the regression-tracking
workflow.

```sh
./adcp-test snapshot save --kind conformance --name baseline --file conformance.json
./adcp-test snapshot diff --before baseline --after candidate
./adcp-test snapshot delete --name old-baseline
```

API: `GET /api/snapshots`, `POST /api/snapshots/save`,
`GET /api/snapshots/diff?before=&after=`, `POST /api/snapshots/delete`.
