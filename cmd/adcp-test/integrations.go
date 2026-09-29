// Subcommands that integrate adcp-test with the outside world: mcp
// (adcp-test as MCP tools over stdio) and specdiff (spec-version
// expectation diffs). Both print JSON to stdout where a report is
// produced; mcp speaks JSON-RPC 2.0 on stdin/stdout instead.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mcpserver"
	"github.com/sujanchalla0510/adcp-test/internal/specdiff"
)

// runMCP serves adcp-test as MCP tools over stdio (JSON-RPC 2.0).
//
//	adcp-test mcp
//
// One JSON-RPC message per line on stdin; responses on stdout; logs on
// stderr. Connect an MCP client (e.g. Claude Code) with:
// {"command": "adcp-test", "args": ["mcp"]}.
func runMCP(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	srv := &mcpserver.Server{
		In:  stdin,
		Out: stdout,
		Log: log.New(os.Stderr, "adcp-test mcp: ", 0),
	}
	if err := srv.Serve(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return 1
	}
	return 0
}

// maxSpecdiffRun bounds the conformance run behind specdiff --target.
const maxSpecdiffRun = 5 * time.Minute

// runSpecdiff diffs two AdCP spec-version expectation surfaces.
//
//	adcp-test specdiff --from 3.1 --to 4.0 [--target <seller-mcp-url>]
//	    [--bearer-token T] [--surface-only]
//
// Without --target (the default), it prints the pure surface diff:
// added/removed/changed tools and auth-requirement changes. With
// --target, it runs conformance against the seller and flags what would
// break under the newer spec's expectations.
func runSpecdiff(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("specdiff", flag.ContinueOnError)
	from := fs.String("from", "3.1", "older spec version (known: 3.1, 4.0)")
	to := fs.String("to", "4.0", "newer spec version (known: 3.1, 4.0)")
	target := fs.String("target", "", "seller MCP endpoint URL: ground the diff against a live run")
	bearer := fs.String("bearer-token", "", "bearer token for the target (optional)")
	surfaceOnly := fs.Bool("surface-only", false, "print only the surface diff, even with --target")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fromS, err := specdiff.Get(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "specdiff:", err)
		return 2
	}
	toS, err := specdiff.Get(*to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "specdiff:", err)
		return 2
	}
	d := specdiff.DiffSurfaces(fromS, toS)

	if *target == "" || *surfaceOnly {
		writeJSONReport(stdout, d)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxSpecdiffRun)
	defer cancel()
	rep, err := specdiff.DiffAgainstTarget(ctx, *target, *from, *to, *bearer)
	if err != nil {
		writeCIError(stdout, err.Error())
		return 1
	}
	writeJSONReport(stdout, rep)
	for _, f := range rep.Findings {
		if f.Severity == "would-fail" {
			return 1
		}
	}
	return 0
}
