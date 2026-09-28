// Command adcp-test is the all-in-one AdCP integration test suite.
//
// It serves a local web UI (UI-first; localhost only). The headless --ci
// mode runs the conformance suite against --target and prints the JSON
// report to stdout: exit 0 when every check passes, 1 otherwise.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/config"
	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/server"
)

// maxCIRun bounds a headless conformance run.
const maxCIRun = 3 * time.Minute

func main() {
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
