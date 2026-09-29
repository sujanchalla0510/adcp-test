package scenarios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/session"
)

func runPackWithChaos(t *testing.T, packID string, chaos *ChaosOptions) *Report {
	t.Helper()
	url := packTestServer(t)
	p, err := LoadBuiltin(packID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rep, err := Run(ctx, p, url, Options{Store: session.NewStore(), Chaos: chaos})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestChaosDroppedCalls: with fault_rate=1 every step is dropped before
// send; each step is labeled and the report records the seed.
func TestChaosDroppedCalls(t *testing.T) {
	rep := runPackWithChaos(t, "cancel-pause", &ChaosOptions{Enabled: true, Seed: 42, FaultRate: 1})
	if rep.Chaos == nil {
		t.Fatal("expected a chaos summary in the report")
	}
	if rep.Chaos.Seed != 42 {
		t.Errorf("expected seed 42 recorded, got %d", rep.Chaos.Seed)
	}
	total := 0
	for _, sc := range rep.Scenarios {
		for _, st := range sc.Steps {
			total++
			if st.Chaos == nil || st.Chaos.Injected != "dropped-call" {
				t.Errorf("step %q: expected dropped-call mark, got %+v", st.Name, st.Chaos)
			}
			if st.Passed {
				t.Errorf("step %q: a dropped call must fail its assertions", st.Name)
			}
		}
	}
	if rep.Chaos.InjectedFaults != total || total == 0 {
		t.Errorf("expected %d injected faults, got %d", total, rep.Chaos.InjectedFaults)
	}
	if rep.Chaos.StepsSurvived != 0 {
		t.Errorf("expected 0 survivors, got %d", rep.Chaos.StepsSurvived)
	}
}

// TestChaosLatencySpikes: spikes are labeled and steps still pass.
func TestChaosLatencySpikes(t *testing.T) {
	rep := runPackWithChaos(t, "cancel-pause",
		&ChaosOptions{Enabled: true, Seed: 7, LatencySpikeP: 1, MaxSpikeMs: 5})
	if rep.Chaos == nil {
		t.Fatal("expected a chaos summary in the report")
	}
	total := 0
	for _, sc := range rep.Scenarios {
		for _, st := range sc.Steps {
			total++
			if st.Chaos == nil || st.Chaos.Injected != "latency-spike" {
				t.Errorf("step %q: expected latency-spike mark, got %+v", st.Name, st.Chaos)
			}
		}
	}
	if rep.Chaos.InjectedSpikes != total {
		t.Errorf("expected %d injected spikes, got %d", total, rep.Chaos.InjectedSpikes)
	}
	if rep.Chaos.StepsSurvived != total {
		t.Errorf("expected all %d steps to survive spikes, got %d", total, rep.Chaos.StepsSurvived)
	}
	if !rep.AllPassed() {
		t.Errorf("pack should pass under spikes alone:\n%s", reportFailures(rep))
	}
}

// TestChaosReproducible: the same seed injects the same pattern twice.
func TestChaosReproducible(t *testing.T) {
	chaos := &ChaosOptions{Enabled: true, Seed: 99}
	a := runPackWithChaos(t, "cancel-pause", chaos)
	b := runPackWithChaos(t, "cancel-pause", chaos)
	var marksA, marksB []string
	for _, rep := range []*Report{a, b} {
		var marks []string
		for _, sc := range rep.Scenarios {
			for _, st := range sc.Steps {
				if st.Chaos != nil {
					marks = append(marks, st.Chaos.Injected)
				} else {
					marks = append(marks, "-")
				}
			}
		}
		if rep == a {
			marksA = marks
		} else {
			marksB = marks
		}
	}
	if len(marksA) != len(marksB) {
		t.Fatalf("mark counts differ: %d vs %d", len(marksA), len(marksB))
	}
	for i := range marksA {
		if marksA[i] != marksB[i] {
			t.Fatalf("seed 99 not reproducible: %v vs %v", marksA, marksB)
		}
	}
}

// TestChaosDisabledByDefault: no chaos section without the option.
func TestChaosDisabledByDefault(t *testing.T) {
	rep := runPackWithChaos(t, "cancel-pause", nil)
	if rep.Chaos != nil {
		t.Errorf("expected no chaos summary, got %+v", rep.Chaos)
	}
	for _, sc := range rep.Scenarios {
		for _, st := range sc.Steps {
			if st.Chaos != nil {
				t.Errorf("step %q unexpectedly chaos-marked", st.Name)
			}
		}
	}
}

// TestChaosRefusesRemoteTarget: chaos injection against a non-localhost
// target is refused without AllowRemote — dropping calls at a remote
// seller would be a denial of service.
func TestChaosRefusesRemoteTarget(t *testing.T) {
	p, err := LoadBuiltin("cancel-pause")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = Run(ctx, p, "https://seller.example/mcp", Options{
		Store: session.NewStore(),
		Chaos: &ChaosOptions{Enabled: true, Seed: 1},
	})
	if err == nil {
		t.Fatal("expected chaos run against a remote target to be refused")
	}
	if !strings.Contains(err.Error(), "non-localhost") {
		t.Fatalf("expected a localhost-guard error, got: %v", err)
	}
}

// TestChaosRemoteAllowedWithOverride: AllowRemote bypasses the guard; the
// run then proceeds and fails on transport (unreachable host), not on the
// guard — proving the override path works.
func TestChaosRemoteAllowedWithOverride(t *testing.T) {
	p, err := LoadBuiltin("cancel-pause")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The guard is bypassed, so Run proceeds and the steps fail on
	// transport (unreachable host) — recorded in the report, not as a
	// Run error.
	rep, err := Run(ctx, p, "https://seller.example/mcp", Options{
		Store:       session.NewStore(),
		Chaos:       &ChaosOptions{Enabled: true, Seed: 1},
		AllowRemote: true,
	})
	if err != nil {
		t.Fatalf("guard should have been bypassed, got: %v", err)
	}
	if rep.AllPassed() {
		t.Error("expected the unreachable host to fail the run")
	}
}

// TestChaosAllowsLocalhost: chaos runs against loopback targets are
// accepted without any override.
func TestChaosAllowsLocalhost(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:1/mcp", "http://localhost:1/mcp"} {
		p, err := LoadBuiltin("cancel-pause")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		// Unreachable port: Run succeeds (steps fail on transport, recorded
		// in the report), which proves the localhost guard did not reject
		// the target.
		rep, err := Run(ctx, p, target, Options{
			Store: session.NewStore(),
			Chaos: &ChaosOptions{Enabled: true, Seed: 1},
		})
		cancel()
		if err != nil {
			t.Fatalf("localhost target %s was refused: %v", target, err)
		}
		if rep.AllPassed() {
			t.Errorf("expected transport failures for %s", target)
		}
	}
}

// TestNoChaosNoGuard: ordinary scenario runs (no chaos) are not subject to
// the localhost guard — real sellers live on the public internet.
func TestNoChaosNoGuard(t *testing.T) {
	p, err := LoadBuiltin("cancel-pause")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Unreachable host: the guard must not fire; the steps fail on
	// transport instead (recorded in the report).
	rep, err := Run(ctx, p, "https://seller.example/mcp", Options{Store: session.NewStore()})
	if err != nil {
		t.Fatalf("non-chaos run must not hit the chaos localhost guard: %v", err)
	}
	if rep.AllPassed() {
		t.Error("expected transport failures for the unreachable host")
	}
}
