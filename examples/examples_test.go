// Package examples_test guards the worked-example fixtures in
// examples/README.md: the mock seller config must parse, serve the full
// required AdCP tool surface, pass conformance headlessly, and run the
// happy-path pack to completion. Synthetic fixtures only.
package examples_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/conformance"
	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
)

func startExampleSeller(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("mock-seller.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	cfg, err := mockcfg.Parse(raw)
	if err != nil {
		t.Fatalf("parse example config: %v", err)
	}
	svcs := cfg.Services()
	if len(svcs) != 1 {
		t.Fatalf("expected 1 example service, got %d", len(svcs))
	}
	srv, err := mockserver.New(svcs[0], mockserver.Options{BaseDir: "."})
	if err != nil {
		t.Fatalf("build example mock server: %v", err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("start example mock server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return url
}

// TestExampleSellerPassesConformance is the README worked example's step 2:
// the synthetic seller passes the full headless conformance suite.
func TestExampleSellerPassesConformance(t *testing.T) {
	url := startExampleSeller(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep, err := conformance.Run(ctx, url, conformance.Options{})
	if err != nil {
		t.Fatalf("conformance run: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Status == conformance.StatusFail {
			t.Errorf("conformance check %q failed: %s", c.Name, c.Detail)
		}
	}
	if len(rep.Checks) == 0 {
		t.Error("conformance produced no checks")
	}
}

// TestExampleSellerRunsHappyPath is the README worked example's step 3:
// the happy-path scenario pack completes against the example seller.
func TestExampleSellerRunsHappyPath(t *testing.T) {
	url := startExampleSeller(t)
	pack, err := scenarios.LoadBuiltin("happy-path-media-buy")
	if err != nil {
		t.Fatalf("load happy-path pack: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rep, err := scenarios.Run(ctx, pack, url, scenarios.Options{})
	if err != nil {
		t.Fatalf("scenario run: %v", err)
	}
	for _, sc := range rep.Scenarios {
		if !sc.Passed {
			for _, st := range sc.Steps {
				if !st.Passed {
					t.Errorf("scenario %q step %q failed", sc.Name, st.Name)
				}
			}
		}
	}
}
