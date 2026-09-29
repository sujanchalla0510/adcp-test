package scenarios

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ChaosOptions configures fault/latency injection layered over a pack
// run. Every injection is labeled on the affected step and the seed is
// recorded in the report, so a chaos run is reproducible.
type ChaosOptions struct {
	// Enabled turns chaos injection on.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Seed seeds the injector; 0 picks a random seed (recorded in the
	// report for reproducibility).
	Seed int64 `yaml:"seed,omitempty" json:"seed,omitempty"`
	// FaultRate is the per-call probability of a dropped call
	// (simulated network failure before send). Default 0.15.
	FaultRate float64 `yaml:"fault_rate,omitempty" json:"fault_rate,omitempty"`
	// LatencySpikeP is the per-call probability of an injected latency
	// spike before send. Default 0.25.
	LatencySpikeP float64 `yaml:"latency_spike_p,omitempty" json:"latency_spike_p,omitempty"`
	// MaxSpikeMs bounds an injected spike. Default 1500.
	MaxSpikeMs int `yaml:"max_spike_ms,omitempty" json:"max_spike_ms,omitempty"`
}

// isLocalhost reports whether raw targets a loopback address. Chaos fault
// injection may only run against localhost unless explicitly allowed —
// dropping calls against a remote seller would be a denial of service.
func isLocalhost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	if host == "" {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// ChaosMark labels a step that chaos touched.
type ChaosMark struct {
	// Injected is "latency-spike" or "dropped-call".
	Injected string `json:"injected"`
	Detail   string `json:"detail"`
}

// ChaosSummary aggregates a run's injections for the report.
type ChaosSummary struct {
	Seed           int64   `json:"seed"`
	FaultRate      float64 `json:"fault_rate"`
	LatencySpikeP  float64 `json:"latency_spike_p"`
	MaxSpikeMs     int     `json:"max_spike_ms"`
	InjectedFaults int     `json:"injected_faults"`
	InjectedSpikes int     `json:"injected_spikes"`
	StepsSurvived  int     `json:"steps_survived"`
	TotalSteps     int     `json:"total_steps"`
}

// defaults fills unset chaos knobs. When only Enabled/Seed are set, the
// standard profile applies (15% drops, 25% spikes, 1500ms cap);
// otherwise each knob is honored literally (0 disables that knob).
func (o *ChaosOptions) defaults() ChaosOptions {
	out := *o
	if out.FaultRate == 0 && out.LatencySpikeP == 0 && out.MaxSpikeMs == 0 {
		out.FaultRate, out.LatencySpikeP, out.MaxSpikeMs = 0.15, 0.25, 1500
	}
	if out.FaultRate < 0 {
		out.FaultRate = 0
	}
	if out.FaultRate > 1 {
		out.FaultRate = 1
	}
	if out.LatencySpikeP < 0 {
		out.LatencySpikeP = 0
	}
	if out.LatencySpikeP > 1 {
		out.LatencySpikeP = 1
	}
	if out.MaxSpikeMs <= 0 {
		out.MaxSpikeMs = 1500
	}
	if out.Seed == 0 {
		out.Seed = time.Now().UnixNano()
	}
	return out
}

// chaosInjector rolls per-call dice from a seeded RNG.
type chaosInjector struct {
	cfg ChaosOptions
	rng *rand.Rand

	mu     sync.Mutex
	faults int
	spikes int
}

func newChaosInjector(o *ChaosOptions) *chaosInjector {
	if o == nil || !o.Enabled {
		return nil
	}
	cfg := o.defaults()
	return &chaosInjector{cfg: cfg, rng: rand.New(rand.NewSource(cfg.Seed))}
}

// beforeCall maybe injects a spike (sleeps, ctx-aware) or a dropped
// call. It returns the mark when it injected something, nil otherwise.
func (c *chaosInjector) beforeCall(ctx context.Context) *ChaosMark {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rng.Float64() < c.cfg.FaultRate {
		c.faults++
		return &ChaosMark{
			Injected: "dropped-call",
			Detail:   "chaos: injected dropped call — the request was discarded before send (simulated network failure)",
		}
	}
	if c.rng.Float64() < c.cfg.LatencySpikeP {
		ms := 50 + c.rng.Intn(c.cfg.MaxSpikeMs)
		c.spikes++
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(ms) * time.Millisecond):
		}
		return &ChaosMark{
			Injected: "latency-spike",
			Detail:   fmt.Sprintf("chaos: injected %d ms latency spike before send", ms),
		}
	}
	return nil
}

// summary builds the report summary; survived counts injected steps
// that still passed.
func (c *chaosInjector) summary(survived, total int) *ChaosSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &ChaosSummary{
		Seed:           c.cfg.Seed,
		FaultRate:      c.cfg.FaultRate,
		LatencySpikeP:  c.cfg.LatencySpikeP,
		MaxSpikeMs:     c.cfg.MaxSpikeMs,
		InjectedFaults: c.faults,
		InjectedSpikes: c.spikes,
		StepsSurvived:  survived,
		TotalSteps:     total,
	}
}
