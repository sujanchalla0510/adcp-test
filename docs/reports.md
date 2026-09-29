# Evidence reports

Assemble a self-contained evidence pack from run reports: an HTML bundle
with inline CSS (no external requests), plus an optional PDF export.
Include a snapshot diff section for before/after evidence.

```sh
./adcp-test report --conformance c.json --scenarios s.json --load l.json \
    --lifecycle lc.json --fuzz f.json --diff-before baseline --diff-after candidate \
    --title "Seller A — integration evidence" \
    --out evidence.html --pdf evidence.pdf
```

Any input may be omitted. API: `POST /api/reports/build` with
`"format": "html"|"pdf"` returns the pack as a download.
