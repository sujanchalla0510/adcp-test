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
	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
	"github.com/sujanchalla0510/adcp-test/internal/recorder"
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
		}
	}

	cfg := config.FromFlags(flag.CommandLine)
	flag.Parse()

	if cfg.CIMode {
		os.Exit(runConformanceCI(os.Stdout, cfg.Target))
	}

	srv := server.New(cfg)
	fmt.Printf("adcp-test serving on http://%s\n", srv.Addr())
	log.Fatal(srv.ListenAndServe())
}

// runConformanceCI runs the conformance suite headless against target,
// writes the JSON report to w, and returns the process exit code:
// 0 when every check passes, 1 otherwise.
func runConformanceCI(w io.Writer, target string) int {
	ctx, cancel := context.WithTimeout(context.Background(), maxCIRun)
	defer cancel()
	rep, err := conformance.Run(ctx, target, conformance.Options{})
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
