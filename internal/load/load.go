// Package load is a concurrent load engine for MCP seller endpoints.
//
// A Config describes what to hammer (one tool call or a scenario pack),
// with how many virtual users, over what ramp-up, for how long or how
// many total iterations. The engine runs a worker pool, collects
// per-request latencies, and reports percentiles, throughput, and error
// rates with CI threshold verdicts.
//
// Safety: targets that do not resolve to localhost are refused unless
// AllowRemote is set explicitly, so a typo cannot turn a load test into
// an accidental production hammering.
package load

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sujanchalla0510/adcp-test/internal/mcpclient"
	"github.com/sujanchalla0510/adcp-test/internal/scenarios"
	"github.com/sujanchalla0510/adcp-test/internal/session"
)

// Config is a load test definition (YAML and JSON serializable).
type Config struct {
	TargetURL string `yaml:"target_url" json:"target_url"`
	// Exactly one of Tool / Scenario selects the workload.
	Tool         string         `yaml:"tool,omitempty" json:"tool,omitempty"`
	Arguments    map[string]any `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Scenario     string         `yaml:"scenario,omitempty" json:"scenario,omitempty"`           // pack file path or built-in id
	ScenarioName string         `yaml:"scenario_name,omitempty" json:"scenario_name,omitempty"` // default: first scenario
	// Shape of the load.
	Concurrency int           `yaml:"concurrency" json:"concurrency"`
	RampUp      time.Duration `yaml:"ramp_up,omitempty" json:"ramp_up,omitempty"`
	Duration    time.Duration `yaml:"duration,omitempty" json:"duration,omitempty"`
	Iterations  int           `yaml:"iterations,omitempty" json:"iterations,omitempty"`
	// Per-request timeout.
	RequestTimeout time.Duration `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
	// AllowRemote bypasses the localhost-only guard.
	AllowRemote bool `yaml:"allow_remote,omitempty" json:"allow_remote,omitempty"`
	// Thresholds for the CI verdict.
	Thresholds Thresholds `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
}

// Thresholds are the CI gates; each is a "must be below" bound.
type Thresholds struct {
	P99MsLt       float64 `yaml:"p99_ms_lt,omitempty" json:"p99_ms_lt,omitempty"`
	ErrorRateLt   float64 `yaml:"error_rate_lt,omitempty" json:"error_rate_lt,omitempty"`
	TimeoutRateLt float64 `yaml:"timeout_rate_lt,omitempty" json:"timeout_rate_lt,omitempty"`
}

// ParseConfig parses load YAML.
func ParseConfig(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("load: parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks the config shape.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.TargetURL) == "" {
		return fmt.Errorf("load: target_url is required")
	}
	if c.Tool == "" && c.Scenario == "" {
		return fmt.Errorf("load: one of tool or scenario is required")
	}
	if c.Tool != "" && c.Scenario != "" {
		return fmt.Errorf("load: tool and scenario are mutually exclusive")
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("load: concurrency must be >= 1")
	}
	if c.Duration <= 0 && c.Iterations <= 0 {
		return fmt.Errorf("load: one of duration or iterations is required")
	}
	if !c.AllowRemote {
		if err := checkLocalhost(c.TargetURL); err != nil {
			return err
		}
	}
	return nil
}

// checkLocalhost refuses non-localhost targets.
func checkLocalhost(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("load: bad target_url %q: %w", raw, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("load: target_url %q has no host", raw)
	}
	if host == "localhost" || host == "::1" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("load: refusing non-localhost target %q (pass allow_remote to override)", raw)
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

// ThresholdResult is one CI gate outcome.
type ThresholdResult struct {
	Name   string  `json:"name"`
	Limit  float64 `json:"limit"`
	Actual float64 `json:"actual"`
	Passed bool    `json:"passed"`
	Detail string  `json:"detail,omitempty"`
}

// Result is the outcome of a load run.
type Result struct {
	Config     Config    `json:"config"`
	StartedAt  time.Time `json:"started_at"`
	DurationMs float64   `json:"duration_ms"`

	TotalRequests int64 `json:"total_requests"`
	Successes     int64 `json:"successes"`
	Errors        int64 `json:"errors"`
	Timeouts      int64 `json:"timeouts"`

	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`

	ThroughputRPS float64 `json:"throughput_rps"`
	ErrorRate     float64 `json:"error_rate"`
	TimeoutRate   float64 `json:"timeout_rate"`

	Thresholds []ThresholdResult `json:"thresholds"`
	Passed     bool              `json:"passed"`
}

// Progress is a live snapshot emitted while the run is in flight.
type Progress struct {
	ElapsedMs     float64 `json:"elapsed_ms"`
	Completed     int64   `json:"completed"`
	Errors        int64   `json:"errors"`
	Timeouts      int64   `json:"timeouts"`
	ThroughputRPS float64 `json:"throughput_rps"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
	Done          bool    `json:"done"`
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// progressInterval is how often the engine emits Progress snapshots.
const progressInterval = 500 * time.Millisecond

// Engine runs a load Config.
type Engine struct {
	cfg *Config
}

// New builds an Engine. The config is validated up front.
func New(cfg *Config) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("load: nil config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Engine{cfg: cfg}, nil
}

// Run executes the load test. Progress snapshots stream on progress
// (nil disables); the channel is closed when the run finishes.
func (e *Engine) Run(ctx context.Context, progress chan<- Progress) (*Result, error) {
	cfg := e.cfg
	reqTimeout := cfg.RequestTimeout
	if reqTimeout <= 0 {
		reqTimeout = mcpclient.DefaultTimeout
	}
	client := &mcpclient.Client{Endpoint: cfg.TargetURL, Timeout: reqTimeout}

	// Scenario mode: resolve the pack once, run the named (or first)
	// scenario per iteration.
	var pack *scenarios.Pack
	scenarioIdx := 0
	if cfg.Scenario != "" {
		var err error
		pack, err = scenarios.ResolvePack(cfg.Scenario)
		if err != nil {
			return nil, err
		}
		if cfg.ScenarioName != "" {
			found := false
			for i, s := range pack.Scenarios {
				if s.Name == cfg.ScenarioName {
					scenarioIdx = i
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("load: scenario %q not found in pack %q", cfg.ScenarioName, pack.Name)
			}
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if cfg.Duration > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, cfg.Duration)
		defer stop()
	}

	var issued atomic.Int64
	var completed, errs, timeouts atomic.Int64
	var mu sync.Mutex
	latencies := make([]float64, 0, 1024)

	work := func() (latencyMs float64, timedOut, failed bool) {
		start := time.Now()
		if pack != nil {
			srep, err := scenarios.Run(ctx, &scenarios.Pack{
				Name:        pack.Name,
				Description: pack.Description,
				Scenarios:   []scenarios.Scenario{pack.Scenarios[scenarioIdx]},
			}, cfg.TargetURL, scenarios.Options{Timeout: reqTimeout, Store: session.NewStore()})
			ms := float64(time.Since(start)) / float64(time.Millisecond)
			if err != nil {
				return ms, isTimeout(err), true
			}
			return ms, false, !srep.AllPassed()
		}
		res, err := client.CallTool(ctx, cfg.Tool, cfg.Arguments)
		ms := float64(time.Since(start)) / float64(time.Millisecond)
		if err != nil {
			return ms, isTimeout(err), true
		}
		return ms, false, res.IsError
	}

	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			// Linear ramp: worker i starts after i/N of the ramp period.
			if cfg.RampUp > 0 && cfg.Concurrency > 1 {
				delay := time.Duration(worker) * cfg.RampUp / time.Duration(cfg.Concurrency)
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}
			}
			for {
				if cfg.Iterations > 0 && issued.Add(1) > int64(cfg.Iterations) {
					return
				}
				select {
				case <-ctx.Done():
					return
				default:
				}
				ms, to, failed := work()
				completed.Add(1)
				if to {
					timeouts.Add(1)
				} else if failed {
					errs.Add(1)
				}
				mu.Lock()
				latencies = append(latencies, ms)
				mu.Unlock()
			}
		}(w)
	}

	// Progress pump.
	done := make(chan struct{})
	if progress != nil {
		go func() {
			t := time.NewTicker(progressInterval)
			defer t.Stop()
			start := time.Now()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					progress <- snapshot(start, &completed, &errs, &timeouts, &mu, latencies, false)
				}
			}
		}()
	}

	startedAt := time.Now()
	wg.Wait()
	close(done)
	elapsed := time.Since(startedAt)
	if progress != nil {
		progress <- snapshot(startedAt, &completed, &errs, &timeouts, &mu, latencies, true)
		close(progress)
	}

	mu.Lock()
	final := append([]float64(nil), latencies...)
	mu.Unlock()

	res := &Result{
		Config:        *cfg,
		StartedAt:     startedAt.UTC(),
		DurationMs:    float64(elapsed) / float64(time.Millisecond),
		TotalRequests: completed.Load(),
		Timeouts:      timeouts.Load(),
		Errors:        errs.Load(),
	}
	// Timeouts are a subset counted separately; successes exclude both.
	res.Successes = res.TotalRequests - res.Errors - res.Timeouts
	if pct := Percentiles(final, 50, 95, 99); len(pct) == 3 {
		res.P50Ms, res.P95Ms, res.P99Ms = pct[0], pct[1], pct[2]
	}
	if secs := elapsed.Seconds(); secs > 0 {
		res.ThroughputRPS = float64(res.TotalRequests) / secs
	}
	if res.TotalRequests > 0 {
		res.ErrorRate = float64(res.Errors) / float64(res.TotalRequests)
		res.TimeoutRate = float64(res.Timeouts) / float64(res.TotalRequests)
	}
	res.Thresholds, res.Passed = evalThresholds(cfg.Thresholds, res)
	return res, nil
}

func snapshot(start time.Time, completed, errs, timeouts *atomic.Int64, mu *sync.Mutex, lat []float64, done bool) Progress {
	elapsed := time.Since(start)
	c := completed.Load()
	mu.Lock()
	cp := append([]float64(nil), lat...)
	mu.Unlock()
	var p50, p95, p99 float64
	if pct := Percentiles(cp, 50, 95, 99); len(pct) == 3 {
		p50, p95, p99 = pct[0], pct[1], pct[2]
	}
	var rps float64
	if secs := elapsed.Seconds(); secs > 0 {
		rps = float64(c) / secs
	}
	return Progress{
		ElapsedMs:     float64(elapsed) / float64(time.Millisecond),
		Completed:     c,
		Errors:        errs.Load(),
		Timeouts:      timeouts.Load(),
		ThroughputRPS: rps,
		P50Ms:         p50,
		P95Ms:         p95,
		P99Ms:         p99,
		Done:          done,
	}
}

// isTimeout reports whether err is a client-side timeout.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var te interface{ Timeout() bool }
	if errors.As(err, &te) && te.Timeout() {
		return true
	}
	// mcpclient wraps transport failures; unwrap and look again.
	var merr *mcpclient.Error
	if errors.As(err, &merr) && merr.Kind == mcpclient.KindTransport {
		return isTimeout(merr.Unwrap())
	}
	return false
}

// Percentiles computes nearest-rank percentiles over ms samples.
// Missing ranks return 0; an empty sample returns all zeros.
func Percentiles(ms []float64, ranks ...float64) []float64 {
	out := make([]float64, len(ranks))
	if len(ms) == 0 {
		return out
	}
	sorted := append([]float64(nil), ms...)
	sort.Float64s(sorted)
	n := len(sorted)
	for i, r := range ranks {
		if r <= 0 {
			out[i] = sorted[0]
			continue
		}
		if r >= 100 {
			out[i] = sorted[n-1]
			continue
		}
		k := int((r / 100 * float64(n)) + 0.999999999) // ceil
		if k < 1 {
			k = 1
		}
		if k > n {
			k = n
		}
		out[i] = sorted[k-1]
	}
	return out
}

// evalThresholds checks the configured gates against the result.
func evalThresholds(t Thresholds, r *Result) ([]ThresholdResult, bool) {
	var out []ThresholdResult
	passed := true
	check := func(name string, limit, actual float64) {
		if limit <= 0 {
			return // gate not configured
		}
		tr := ThresholdResult{Name: name, Limit: limit, Actual: actual, Passed: actual < limit}
		if !tr.Passed {
			tr.Detail = fmt.Sprintf("%s %.4g >= limit %.4g", name, actual, limit)
			passed = false
		}
		out = append(out, tr)
	}
	check("p99_ms_lt", t.P99MsLt, r.P99Ms)
	check("error_rate_lt", t.ErrorRateLt, r.ErrorRate)
	check("timeout_rate_lt", t.TimeoutRateLt, r.TimeoutRate)
	if r.TotalRequests == 0 {
		passed = false
		out = append(out, ThresholdResult{
			Name: "requests_gt_zero", Limit: 0, Actual: 0,
			Passed: false, Detail: "no requests completed",
		})
	}
	return out, passed
}

// Preset is a named starter config for the UI.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	YAML        string `json:"yaml"`
}

// Presets returns the built-in load presets.
func Presets() []Preset {
	return []Preset{
		{
			ID:          "smoke",
			Name:        "Smoke",
			Description: "10 requests, 2 virtual users — quick sanity check.",
			YAML: `target_url: http://127.0.0.1:8080
tool: get_products
concurrency: 2
iterations: 10
request_timeout: 10s
thresholds:
  p99_ms_lt: 2000
  error_rate_lt: 0.01
`,
		},
		{
			ID:          "ramp",
			Name:        "Ramp",
			Description: "30s ramp from 1 to 20 users against get_products.",
			YAML: `target_url: http://127.0.0.1:8080
tool: get_products
concurrency: 20
ramp_up: 30s
duration: 60s
request_timeout: 10s
thresholds:
  p99_ms_lt: 2000
  error_rate_lt: 0.01
`,
		},
		{
			ID:          "soak",
			Name:        "Soak",
			Description: "5 minutes at 10 users — steady-state behavior.",
			YAML: `target_url: http://127.0.0.1:8080
tool: get_products
concurrency: 10
duration: 5m
request_timeout: 10s
thresholds:
  p99_ms_lt: 2000
  error_rate_lt: 0.01
`,
		},
	}
}

// FindPreset returns the preset with id.
func FindPreset(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
