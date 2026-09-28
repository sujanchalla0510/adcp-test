// Command adcp-test is the all-in-one AdCP integration test suite.
//
// It serves a local web UI (UI-first; localhost only). The headless --ci
// mode exists for CI pipelines and prints a JSON status without serving.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/sujanchalla0510/adcp-test/internal/config"
	"github.com/sujanchalla0510/adcp-test/internal/server"
)

func main() {
	cfg := config.FromFlags(flag.CommandLine)
	flag.Parse()

	if cfg.CIMode {
		fmt.Println(`{"status":"ok","mode":"ci"}`)
		os.Exit(0)
	}

	srv := server.New(cfg)
	fmt.Printf("adcp-test serving on http://%s\n", srv.Addr())
	log.Fatal(srv.ListenAndServe())
}
