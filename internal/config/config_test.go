package config

import (
	"flag"
	"testing"
)

func TestDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := FromFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if c.Port != DefaultPort {
		t.Fatalf("default port = %d, want %d", c.Port, DefaultPort)
	}
	if c.CIMode {
		t.Fatal("default CIMode = true, want false")
	}
	if c.Target != "" {
		t.Fatalf("default Target = %q, want empty", c.Target)
	}
}

func TestOverrides(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := FromFlags(fs)
	if err := fs.Parse([]string{"--port", "9999", "--ci", "--target", "https://seller.example/mcp"}); err != nil {
		t.Fatal(err)
	}
	if c.Port != 9999 {
		t.Fatalf("port = %d, want 9999", c.Port)
	}
	if !c.CIMode {
		t.Fatal("CIMode = false, want true")
	}
	if c.Target != "https://seller.example/mcp" {
		t.Fatalf("Target = %q, want the example URL", c.Target)
	}
}
