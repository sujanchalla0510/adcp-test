// M6 subcommands: signdebug, lifecycle, fuzz, webhook-listen, snapshot,
// report. Each prints JSON to stdout and exits 0 on success / 1 on
// failure, so they compose with CI the same way scenario/load do.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/fuzz"
	"github.com/sujanchalla0510/adcp-test/internal/lifecycle"
	"github.com/sujanchalla0510/adcp-test/internal/reports"
	"github.com/sujanchalla0510/adcp-test/internal/signdebug"
	"github.com/sujanchalla0510/adcp-test/internal/snapshots"
	"github.com/sujanchalla0510/adcp-test/internal/webhooks"
)

// maxHeadlessRun bounds the headless M6 subcommands.
const maxHeadlessRun = 5 * time.Minute

func writeJSONReport(stdout io.Writer, v any) {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// runSigndebug verifies an RFC 9421 signature offline.
//
//	adcp-test signdebug --request req.json --key key.pem [--expected-base base.txt]
func runSigndebug(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("signdebug", flag.ContinueOnError)
	reqPath := fs.String("request", "", "JSON file with {method, url, headers, body} (required)")
	keyPath := fs.String("key", "", "PEM-encoded public or private key file (required)")
	basePath := fs.String("expected-base", "", "file with the signer-computed signature base, for diffing (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reqPath == "" || *keyPath == "" {
		fs.Usage()
		return 2
	}
	reqData, err := os.ReadFile(*reqPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "signdebug:", err)
		return 1
	}
	var req signdebug.Request
	if err := json.Unmarshal(reqData, &req); err != nil {
		fmt.Fprintln(os.Stderr, "signdebug: invalid request JSON:", err)
		return 1
	}
	keyPEM, err := os.ReadFile(*keyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "signdebug:", err)
		return 1
	}
	var expected string
	if *basePath != "" {
		b, err := os.ReadFile(*basePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "signdebug:", err)
			return 1
		}
		expected = string(b)
	}
	rep := signdebug.Verify(&req, keyPEM, signdebug.Options{ExpectedBase: expected})
	writeJSONReport(stdout, rep)
	if rep.Verdict == "valid" {
		return 0
	}
	return 1
}

// runLifecycle walks one media buy through its lifecycle.
//
//	adcp-test lifecycle --target <seller-mcp-url> [--bearer-token T]
func runLifecycle(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("lifecycle", flag.ContinueOnError)
	target := fs.String("target", "", "seller MCP endpoint URL (required)")
	bearer := fs.String("bearer-token", "", "bearer token for the target (optional)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHeadlessRun)
	defer cancel()
	rep, err := lifecycle.Run(ctx, *target, lifecycle.Options{BearerToken: *bearer})
	if err != nil {
		writeCIError(stdout, err.Error())
		return 1
	}
	writeJSONReport(stdout, rep)
	if rep.AllPassed() {
		return 0
	}
	return 1
}

// runFuzz throws malformed JSON-RPC payloads at the target.
//
//	adcp-test fuzz --target <url> [--iterations N] [--seed S] [--allow-remote]
func runFuzz(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("fuzz", flag.ContinueOnError)
	target := fs.String("target", "", "seller MCP endpoint URL (required)")
	iterations := fs.Int("iterations", 200, "number of payloads to send")
	seed := fs.Int64("seed", 0, "payload generator seed (0 = random)")
	timeout := fs.Duration("timeout", 10*time.Second, "per-request timeout")
	bearer := fs.String("bearer-token", "", "bearer token for the target (optional)")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fs.Usage()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxHeadlessRun)
	defer cancel()
	rep, err := fuzz.Run(ctx, fuzz.Options{
		Target:      *target,
		Iterations:  *iterations,
		Seed:        *seed,
		Timeout:     *timeout,
		BearerToken: *bearer,
		AllowRemote: *allowRemote,
	})
	if err != nil {
		writeCIError(stdout, err.Error())
		return 1
	}
	writeJSONReport(stdout, rep)
	if rep.Passed() {
		return 0
	}
	return 1
}

// runWebhookListen captures seller webhooks on localhost.
//
//	adcp-test webhook-listen [--port P] [--out deliveries.json]
func runWebhookListen(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("webhook-listen", flag.ContinueOnError)
	port := fs.Int("port", 0, "localhost port to bind (0 = pick a free one)")
	out := fs.String("out", "", "write captured deliveries as JSON on exit (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	l := webhooks.NewListener(0)
	url, err := l.Start(*port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "webhook-listen:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "webhook-listen: capturing at %s (Ctrl-C to stop)\n", url)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Fprintln(os.Stderr, "\nwebhook-listen: stopped")
	_ = l.Close()
	deliveries := l.Deliveries()
	if deliveries == nil {
		deliveries = []*webhooks.Delivery{}
	}
	outW := stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "webhook-listen:", err)
			return 1
		}
		defer f.Close()
		outW = f
	}
	writeJSONReport(outW, map[string]any{"url": url, "deliveries": deliveries})
	return 0
}

// runSnapshot manages the ./snapshots store.
//
//	adcp-test snapshot save --kind conformance --name baseline --file report.json
//	adcp-test snapshot list
//	adcp-test snapshot diff --before a --after b
//	adcp-test snapshot delete --name a
func runSnapshot(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "snapshot: subcommand required: save|list|diff|delete")
		return 2
	}
	store := snapshots.New("")
	switch args[0] {
	case "save":
		fs := flag.NewFlagSet("snapshot save", flag.ContinueOnError)
		kind := fs.String("kind", "", "report kind: conformance|scenario|load|lifecycle|fuzz (required)")
		name := fs.String("name", "", "snapshot name (required)")
		file := fs.String("file", "", "report JSON file (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *kind == "" || *name == "" || *file == "" {
			fs.Usage()
			return 2
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot save:", err)
			return 1
		}
		entry, err := store.Save(*kind, *name, data)
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot save:", err)
			return 1
		}
		writeJSONReport(stdout, map[string]any{"entry": entry})
		return 0
	case "list":
		list, err := store.List()
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot list:", err)
			return 1
		}
		if list == nil {
			list = []*snapshots.Entry{}
		}
		writeJSONReport(stdout, map[string]any{"snapshots": list})
		return 0
	case "diff":
		fs := flag.NewFlagSet("snapshot diff", flag.ContinueOnError)
		before := fs.String("before", "", "older snapshot name (required)")
		after := fs.String("after", "", "newer snapshot name (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *before == "" || *after == "" {
			fs.Usage()
			return 2
		}
		d, err := store.DiffSnapshots(*before, *after)
		if err != nil {
			fmt.Fprintln(os.Stderr, "snapshot diff:", err)
			return 1
		}
		writeJSONReport(stdout, d)
		return 0
	case "delete":
		fs := flag.NewFlagSet("snapshot delete", flag.ContinueOnError)
		name := fs.String("name", "", "snapshot name (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *name == "" {
			fs.Usage()
			return 2
		}
		if err := store.Delete(*name); err != nil {
			fmt.Fprintln(os.Stderr, "snapshot delete:", err)
			return 1
		}
		writeJSONReport(stdout, map[string]any{"status": "ok"})
		return 0
	default:
		fmt.Fprintln(os.Stderr, "snapshot: unknown subcommand", args[0])
		return 2
	}
}

// runReport builds an evidence pack from saved report JSON files.
//
//	adcp-test report --conformance c.json --scenarios s.json --load l.json \
//	    --lifecycle lc.json --fuzz f.json --diff-before a --diff-after b \
//	    --out evidence.html [--pdf evidence.pdf] [--title "..."]
func runReport(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	title := fs.String("title", "", "evidence pack title")
	conformance := fs.String("conformance", "", "conformance report JSON file")
	scenarioFile := fs.String("scenarios", "", "scenario report JSON file")
	loadFile := fs.String("load", "", "load report JSON file")
	lifecycleFile := fs.String("lifecycle", "", "lifecycle report JSON file")
	fuzzFile := fs.String("fuzz", "", "fuzz report JSON file")
	diffBefore := fs.String("diff-before", "", "older snapshot name for the diff section")
	diffAfter := fs.String("diff-after", "", "newer snapshot name for the diff section")
	out := fs.String("out", "", "HTML output file (required)")
	pdfOut := fs.String("pdf", "", "also write a PDF export to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fs.Usage()
		return 2
	}
	readOpt := func(path string) ([]byte, error) {
		if path == "" {
			return nil, nil
		}
		return os.ReadFile(path)
	}
	inputs := reports.Inputs{Title: *title}
	var err error
	if inputs.Conformance, err = readOpt(*conformance); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if inputs.Scenarios, err = readOpt(*scenarioFile); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if inputs.Load, err = readOpt(*loadFile); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if inputs.Lifecycle, err = readOpt(*lifecycleFile); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if inputs.Fuzz, err = readOpt(*fuzzFile); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if *diffBefore != "" && *diffAfter != "" {
		d, err := snapshots.New("").DiffSnapshots(*diffBefore, *diffAfter)
		if err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			return 1
		}
		inputs.Diff = d
	}
	e, err := reports.Build(inputs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	if err := os.WriteFile(*out, []byte(reports.BuildHTML(e)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "report:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", *out)
	if *pdfOut != "" {
		if err := os.WriteFile(*pdfOut, reports.BuildPDF(e), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", *pdfOut)
	}
	return 0
}
