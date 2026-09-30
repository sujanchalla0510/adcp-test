// Package config holds adcp-test runtime configuration.
package config

import "flag"

// DefaultPort is the default localhost port the UI is served on.
const DefaultPort = 18742

// Config is the runtime configuration for adcp-test.
type Config struct {
	// Port is the localhost port to serve the UI on.
	Port int
	// CIMode enables headless CI mode: print a JSON status and exit.
	CIMode bool
	// Target is the seller agent's MCP endpoint URL under test.
	// Used with CIMode and by the conformance UI screen.
	Target string
	// BearerToken is sent as Authorization: Bearer on every probe.
	// Used with CIMode.
	BearerToken string
	// Profile selects the conformance tool-surface profile: full
	// (default), media-buy, creative, or signals. Used with CIMode.
	Profile string
}

// FromFlags registers adcp-test flags on fs and returns the Config that
// will be populated once fs.Parse has run.
func FromFlags(fs *flag.FlagSet) *Config {
	c := &Config{}
	fs.IntVar(&c.Port, "port", DefaultPort, "localhost port to serve the UI on")
	fs.BoolVar(&c.CIMode, "ci", false, "headless CI mode: run conformance against --target, print JSON report, exit 0 on all-pass")
	fs.StringVar(&c.Target, "target", "", "seller agent MCP endpoint URL to test (used with --ci)")
	fs.StringVar(&c.BearerToken, "bearer-token", "", "bearer token for the target (used with --ci)")
	fs.StringVar(&c.Profile, "profile", "full", "conformance tool-surface profile: full, media-buy, creative, or signals (used with --ci)")
	return c
}
