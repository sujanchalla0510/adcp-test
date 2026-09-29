package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunMCPSmoke(t *testing.T) {
	in := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"list_scenarios\",\"arguments\":{}}}\n")
	var out bytes.Buffer
	if code := runMCP([]string{}, in, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d responses, want 3:\n%s", len(lines), out.String())
	}
	var initRes struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initRes); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if initRes.Result.ProtocolVersion == "" {
		t.Error("initialize response missing protocolVersion")
	}
	var listRes struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listRes); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if len(listRes.Result.Tools) != 5 {
		t.Errorf("tools/list returned %d tools, want 5", len(listRes.Result.Tools))
	}
	if !strings.Contains(lines[2], "happy-path-media-buy") {
		t.Errorf("list_scenarios call missing built-in pack:\n%s", lines[2])
	}
}

func TestRunMCPBadFlag(t *testing.T) {
	var out bytes.Buffer
	if code := runMCP([]string{"--bogus"}, strings.NewReader(""), &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 for bad flag", code)
	}
}

func TestRunSpecdiffSurfaceOnly(t *testing.T) {
	var out bytes.Buffer
	if code := runSpecdiff([]string{"--from", "3.1", "--to", "4.0"}, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var d struct {
		From        string `json:"from"`
		To          string `json:"to"`
		ToStatus    string `json:"to_status"`
		ToolChanges []struct {
			Kind string `json:"kind"`
			Tool string `json:"tool"`
		} `json:"tool_changes"`
	}
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	if d.From != "3.1" || d.To != "4.0" {
		t.Errorf("endpoints = %s -> %s, want 3.1 -> 4.0", d.From, d.To)
	}
	if d.ToStatus != "draft-expectation" {
		t.Errorf("to_status = %q, want draft-expectation", d.ToStatus)
	}
	var sawSyncAccounts bool
	for _, c := range d.ToolChanges {
		if c.Tool == "sync_accounts" && c.Kind == "became-required" {
			sawSyncAccounts = true
		}
	}
	if !sawSyncAccounts {
		t.Errorf("missing sync_accounts became-required change: %+v", d.ToolChanges)
	}
}

func TestRunSpecdiffBadVersion(t *testing.T) {
	var out bytes.Buffer
	if code := runSpecdiff([]string{"--from", "3.1", "--to", "9.9"}, &out); code != 2 {
		t.Fatalf("exit code = %d, want 2 for unknown version", code)
	}
}

func TestRunSpecdiffAgainstFake(t *testing.T) {
	srv := minimalFakeSeller(t) // advertises no tools: surface unreadable
	var out bytes.Buffer
	code := runSpecdiff([]string{"--from", "3.1", "--to", "4.0", "--target", srv.URL}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (no would-fail findings when surface unreadable)", code)
	}
	var rep struct {
		Findings []struct {
			Severity string `json:"severity"`
			Check    string `json:"check"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out.String())
	}
	var sawAttention bool
	for _, f := range rep.Findings {
		if f.Severity == "would-fail" {
			t.Errorf("unexpected would-fail finding against unreadable surface: %+v", f)
		}
		if f.Check == "tool-surface" && f.Severity == "attention" {
			sawAttention = true
		}
	}
	if !sawAttention {
		t.Errorf("expected attention finding for unreadable surface: %+v", rep.Findings)
	}
}
