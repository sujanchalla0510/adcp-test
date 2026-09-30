// Command adcp-test is the all-in-one AdCP integration test suite.
//
// It serves a local web UI (UI-first; localhost only). The headless --ci
// mode runs the conformance suite against --target and prints the JSON
// report to stdout: exit 0 when every check passes, 1 otherwise.
//
// Subcommands:
//
//	adcp-test record --upstream <seller-mcp-url> --out session.cassette.json
//	    Start a recording proxy on localhost; point a buyer client at the
//	    printed proxy URL. Ctrl-C stops the proxy and writes the cassette.
//
//	adcp-test mock --config integration.yaml
//	    Run config-driven mock seller services headless. One config file
//	    can define several mocks (compose); each listens on its own
//	    address. A mock with record: proxies the upstream seller and, on
//	    Ctrl-C, writes the generated config to capture_to (or prints it
//	    when capture_to is unset).
//
//	adcp-test scenario --pack happy-path-media-buy --target <seller-mcp-url>
//	    Run a scenario pack headless. Prints the JSON report to stdout:
//	    exit 0 when every scenario passes, 1 otherwise.
//
//	adcp-test load --config load.yaml
//	    Run a load test headless. Prints the JSON report to stdout:
//	    exit 0 when every threshold passes, 1 otherwise. Non-localhost
//	    targets are refused unless --allow-remote is passed.
//
//	adcp-test signdebug --request req.json --key key.pem [--expected-base base.txt]
//	    Verify an RFC 9421 HTTP signature offline. req.json holds
//	    {method, url, headers, body}; the key file holds a PEM public or
//	    private key. Prints the debugger report as JSON: exit 0 when the
//	    signature is valid, 1 otherwise. The key never leaves memory.
//
//	adcp-test lifecycle --target <seller-mcp-url>
//	    Walk one media buy through create -> activate -> pause -> resume ->
//	    cancel, then verify the illegal cancelled -> active transition is
//	    rejected with a structured error. Prints the JSON report: exit 0
//	    when every check passes, 1 otherwise.
//
//	adcp-test fuzz --target <url> [--iterations N] [--seed S]
//	    Throw malformed JSON-RPC payloads at the target and report crashes,
//	    hangs, and non-JSON responses. Localhost only unless --allow-remote.
//	    Exit 0 when the target survived everything, 1 on any finding.
//
//	adcp-test webhook-listen [--port P] [--out deliveries.json]
//	    Capture seller webhooks on localhost. Ctrl-C stops the listener and
//	    writes the captured timeline as JSON.
//
//	adcp-test snapshot save --kind <kind> --name <name> --file report.json
//	adcp-test snapshot list | diff --before a --after b | delete --name a
//	    Save, list, diff, and delete named JSON report snapshots under
//	    ./snapshots/.
//
//	adcp-test report --conformance c.json --scenarios s.json --load l.json \
//	    --out evidence.html [--pdf evidence.pdf]
//	    Build a self-contained HTML evidence pack (plus an optional PDF
//	    export) from run reports.
//
//	adcp-test mcp
//	    Serve adcp-test as MCP tools over stdio (JSON-RPC 2.0): an AI
//	    agent can drive conformance, scenarios, the signing debugger,
//	    and lifecycle checks without shelling out to the CLI.
//
//	adcp-test specdiff --from 3.1 --to 4.0-draft-expectations [--target <seller-mcp-url>]
//	    Diff two AdCP spec-version expectation surfaces (added/removed/
//	    changed tools, auth-requirement changes). With --target, run
//	    conformance and flag what would break under the newer spec.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/cassette"
	"github.com/sujanchalla0510/adcp-test/internal/config"
	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/load"
	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
	"github.com/sujanchalla0510/adcp-test/internal/recorder"
	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
	"github.com/sujanchalla0510/adcp-test/internal/server"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// maxCIRun bounds a headless conformance run.
const maxCIRun = 3 * time.Minute

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "record":
			os.Exit(runRecord(os.Args[2:]))
		case "replay":
			os.Exit(runReplay(os.Args[2:]))
		case "mock":
			os.Exit(runMock(os.Args[2:]))
		case "scenario":
			os.Exit(runScenario(os.Args[2:], os.Stdout))
		case "load":
			os.Exit(runLoad(os.Args[2:], os.Stdout))
		case "signdebug":
			os.Exit(runSigndebug(os.Args[2:], os.Stdout))
		case "lifecycle":
			os.Exit(runLifecycle(os.Args[2:], os.Stdout))
		case "fuzz":
			os.Exit(runFuzz(os.Args[2:], os.Stdout))
		case "webhook-listen":
			os.Exit(runWebhookListen(os.Args[2:], os.Stdout))
		case "snapshot":
			os.Exit(runSnapshot(os.Args[2:], os.Stdout))
		case "report":
			os.Exit(runReport(os.Args[2:], os.Stdout))
		case "mcp":
			os.Exit(runMCP(os.Args[2:], os.Stdin, os.Stdout))
		case "specdiff":
			os.Exit(runSpecdiff(os.Args[2:], os.Stdout))
		}
	}

	cfg := config.FromFlags(flag.CommandLine)
	flag.Parse()

	if cfg.CIMode {
		os.Exit(runConformanceCI(os.Stdout, cfg.Target, cfg.BearerToken, cfg.Profile))
	}

	srv := server.New(cfg)
	fmt.Printf("adcp-test serving on http://%s\n", srv.Addr())
	log.Fatal(srv.ListenAndServe())
}

// runConformanceCI runs the conformance suite headless against target,
// writes the JSON report to w, and returns the process exit code:
// 0 when every check passes, 1 otherwise.
func runConformanceCI(w io.Writer, target, bearerToken, profile string) int {
	ctx, cancel := context.WithTimeout(context.Background(), maxCIRun)
	defer cancel()
	rep, err := conformance.Run(ctx, target, conformance.Options{BearerToken: bearerToken, Profile: profile})
	if err != nil {
		writeCIError(w, err.Error())
		return 1
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rep); err != nil {
		writeCIError(w, "encode conformance report")
		return 1
	}
	if rep.AllPassed() {
		return 0
	}
	return 1
}

func writeCIError(w io.Writer, msg string) {
	b, _ := json.Marshal(msg)
	fmt.Fprintf(w, "{\"status\":\"error\",\"error\":%s}\n", b)
}

// runRecord starts a recording proxy between a buyer client and the
// seller at --upstream. Ctrl-C stops the proxy and writes the recorded
// exchanges to the --out cassette file.
func runRecord(args []string) int {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "seller MCP endpoint URL to record (required)")
	out := fs.String("out", "", "cassette file to write on exit (required)")
	port := fs.Int("port", 0, "localhost port for the recording proxy (0 = ephemeral)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *upstream == "" || *out == "" {
		fs.Usage()
		return 2
	}

	rec, err := recorder.NewRecorder(*upstream, session.NewStore())
	if err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		return 1
	}
	proxyURL, err := rec.Start(fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		return 1
	}
	defer rec.Close()

	fmt.Printf("recording proxy: %s -> %s\n", proxyURL, *upstream)
	fmt.Printf("point your buyer client at %s\n", proxyURL)
	fmt.Printf("send header %s: <id> to pin traffic to a named session (optional)\n", recorder.SessionIDHeader)
	fmt.Println("press Ctrl-C to stop and write the cassette")
	waitForSignal()

	c := cassette.FromSessions(rec.Store.List())
	if len(c.Exchanges) == 0 {
		fmt.Println("no exchanges recorded; nothing written")
		return 0
	}
	if err := c.WriteFile(*out); err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		return 1
	}
	fmt.Printf("wrote %d exchanges to %s\n", len(c.Exchanges), *out)
	return 0
}

// runReplay serves a cassette file as a fake MCP endpoint on localhost
// until Ctrl-C.
func runReplay(args []string) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	path := fs.String("cassette", "", "cassette file to serve (required)")
	port := fs.Int("port", 18743, "localhost port for the replay server")
	strict := fs.Bool("strict", false, "fail closed: arguments must deep-equal the recorded ones")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fs.Usage()
		return 2
	}

	c, err := cassette.LoadFile(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		return 1
	}
	srv := &cassette.Server{Cassette: c, Strict: *strict}
	url, err := srv.Start(fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		return 1
	}
	defer srv.Close()

	fmt.Printf("replaying %s (%d exchanges, strict=%v) on %s\n", *path, len(c.Exchanges), *strict, url)
	fmt.Println("press Ctrl-C to stop")
	waitForSignal()
	return 0
}

// runMock runs config-driven mock seller services headless until Ctrl-C.
func runMock(args []string) int {
	fs := flag.NewFlagSet("mock", flag.ContinueOnError)
	configPath := fs.String("config", "", "mock YAML config file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fs.Usage()
		return 2
	}

	cfg, err := mockcfg.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock:", err)
		return 1
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(os.Stderr, "mock: invalid config:")
		for _, line := range strings.Split(err.Error(), "\n") {
			fmt.Fprintln(os.Stderr, "  "+line)
		}
		return 1
	}

	var srvs []*mockserver.Server
	for _, svc := range cfg.Services() {
		srv, err := mockserver.New(svc, mockserver.Options{BaseDir: cfg.BaseDir()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock %q: %v\n", svc.Name, err)
			return 1
		}
		url, err := srv.Start()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mock %q: %v\n", svc.Name, err)
			return 1
		}
		srvs = append(srvs, srv)
		mode := "mock"
		if srv.IsRecordMode() {
			mode = "record proxy"
		}
		fmt.Printf("mock %-12s %-12s %s\n", svc.Name, mode, url)
	}
	fmt.Println("press Ctrl-C to stop")
	waitForSignal()

	for _, srv := range srvs {
		if srv.IsRecordMode() {
			finishRecordCLI(srv)
		}
		_ = srv.Close()
	}
	return 0
}

// finishRecordCLI generates the mock config from a record-mode server's
// captured traffic: written to capture_to, or printed when unset.
func finishRecordCLI(srv *mockserver.Server) {
	cfg := srv.RecordedConfig()
	if cfg == nil || len(cfg.Services()) == 0 || len(cfg.Services()[0].Routes) == 0 {
		fmt.Printf("mock %-12s no exchanges captured; nothing generated\n", srv.Name())
		return
	}
	data, err := cfg.Marshal()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock %s: generate config: %v\n", srv.Name(), err)
		return
	}
	if path := srv.CaptureTo(); path != "" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "mock %s: write %s: %v\n", srv.Name(), path, err)
			return
		}
		fmt.Printf("mock %-12s wrote %d routes to %s\n", srv.Name(), len(cfg.Services()[0].Routes), path)
		return
	}
	fmt.Printf("mock %-12s generated config:\n%s", srv.Name(), data)
}

// waitForSignal blocks until SIGINT or SIGTERM.
func waitForSignal() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	fmt.Println()
}

// maxScenarioCI bounds a headless scenario run.
const maxScenarioCI = 10 * time.Minute

// runScenario runs a scenario pack headless: JSON report to stdout,
// exit 0 when every scenario passes, 1 otherwise, 2 on usage errors.
//
//	adcp-test scenario --pack happy-path-media-buy --target <seller-mcp-url>
//	adcp-test scenario --list
func runScenario(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("scenario", flag.ContinueOnError)
	packRef := fs.String("pack", "", "built-in pack id or path to a pack YAML file")
	target := fs.String("target", "", "seller MCP endpoint URL")
	bearer := fs.String("bearer-token", "", "bearer token for the target (optional)")
	timeout := fs.Duration("timeout", 0, "per-step request timeout (0 = default 30s)")
	chaos := fs.Bool("chaos", false, "inject random faults/latency spikes into the run")
	chaosSeed := fs.Int64("chaos-seed", 0, "chaos RNG seed (0 = random; recorded in the report)")
	allowRemote := fs.Bool("allow-remote", false, "allow a chaos run against a non-localhost target")
	list := fs.Bool("list", false, "list built-in packs and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *list {
		metas, err := scenarios.BuiltinPacks()
		if err != nil {
			fmt.Fprintln(os.Stderr, "scenario:", err)
			return 1
		}
		for _, m := range metas {
			fmt.Fprintf(stdout, "%-24s %d scenario(s) — %s\n", m.ID, m.ScenarioCount, m.Description)
		}
		return 0
	}
	if *packRef == "" || *target == "" {
		fs.Usage()
		return 2
	}
	pack, err := scenarios.ResolvePack(*packRef)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scenario:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxScenarioCI)
	defer cancel()
	var chaosOpts *scenarios.ChaosOptions
	if *chaos {
		chaosOpts = &scenarios.ChaosOptions{Enabled: true, Seed: *chaosSeed}
	}
	rep, err := scenarios.Run(ctx, pack, *target, scenarios.Options{
		Timeout:     *timeout,
		BearerToken: *bearer,
		Chaos:       chaosOpts,
		AllowRemote: *allowRemote,
	})
	if err != nil {
		writeCIError(stdout, err.Error())
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rep); err != nil {
		writeCIError(stdout, "encode scenario report")
		return 1
	}
	if rep.AllPassed() {
		return 0
	}
	return 1
}

// runLoad runs a load test headless: JSON report to stdout, exit 0 when
// every threshold passes, 1 otherwise, 2 on usage errors.
//
//	adcp-test load --config load.yaml [--allow-remote]
func runLoad(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	configPath := fs.String("config", "", "load YAML config file (required)")
	allowRemote := fs.Bool("allow-remote", false, "allow non-localhost targets")
	target := fs.String("target", "", "override target_url from the config")
	concurrency := fs.Int("concurrency", 0, "override concurrency from the config")
	duration := fs.Duration("duration", 0, "override duration from the config")
	iterations := fs.Int("iterations", 0, "override iterations from the config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		fs.Usage()
		return 2
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		return 1
	}
	cfg, err := load.ParseConfig(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		return 1
	}
	if *allowRemote {
		cfg.AllowRemote = true
	}
	if *target != "" {
		cfg.TargetURL = *target
	}
	if *concurrency > 0 {
		cfg.Concurrency = *concurrency
	}
	if *duration > 0 {
		cfg.Duration = *duration
	}
	if *iterations > 0 {
		cfg.Iterations = *iterations
	}
	eng, err := load.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		return 1
	}

	// Progress to stderr keeps stdout clean JSON for CI.
	progress := make(chan load.Progress, 64)
	go func() {
		for p := range progress {
			fmt.Fprintf(os.Stderr, "\rload: %d req, %.0f rps, p99 %.0f ms, err %d, timeouts %d",
				p.Completed, p.ThroughputRPS, p.P99Ms, p.Errors, p.Timeouts)
		}
		fmt.Fprintln(os.Stderr)
	}()

	res, err := eng.Run(context.Background(), progress)
	if err != nil {
		writeCIError(stdout, err.Error())
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res); err != nil {
		writeCIError(stdout, "encode load report")
		return 1
	}
	if res.Passed {
		return 0
	}
	return 1
}
