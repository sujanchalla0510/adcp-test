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
}

// FromFlags registers adcp-test flags on fs and returns the Config that
// will be populated once fs.Parse has run.
func FromFlags(fs *flag.FlagSet) *Config {
	c := &Config{}
	fs.IntVar(&c.Port, "port", DefaultPort, "localhost port to serve the UI on")
	fs.BoolVar(&c.CIMode, "ci", false, "headless CI mode: print JSON status and exit")
	return c
}
