package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/sujanchalla0510/adcp-test/internal/mockcfg"
	"github.com/sujanchalla0510/adcp-test/internal/mockserver"
)

// scriptedSession feeds requests to a Server and returns the decoded
// responses in order. Notifications (no id) produce no response.
func scriptedSession(t *testing.T, requests []string) []response {
	t.Helper()
	var in bytes.Buffer
	for _, r := range requests {
		in.WriteString(r)
		in.WriteString("\n")
	}
	var out bytes.Buffer
	srv := &Server{In: &in, Out: &out, Log: log.New(&bytes.Buffer{}, "", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := srv.Serve(ctx); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []response
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var r response
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		resps = append(resps, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan responses: %v", err)
	}
	return resps
}

// rpc builds a JSON-RPC request line with a numeric id.
func rpc(id int, method string, params any) string {
	p, _ := json.Marshal(params)
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, p)
}

// notification builds a JSON-RPC notification line (no id).
func notification(method string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":%q}`, method)
}

// call builds a tools/call request for the named tool.
func call(id int, name string, args any) string {
	a, _ := json.Marshal(args)
	return rpc(id, "tools/call", map[string]any{"name": name, "arguments": json.RawMessage(a)})
}

// callText extracts the concatenated text content of a tools/call result.
func callText(t *testing.T, r response) string {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", r.Error)
	}
	raw, _ := json.Marshal(r.Result)
	var cr callResult
	if err := json.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("decode call result: %v", err)
	}
	var sb strings.Builder
	for _, c := range cr.Content {
		sb.WriteString(c.Text)
	}
	return sb.String()
}

func TestProtocolHandshake(t *testing.T) {
	resps := scriptedSession(t, []string{
		rpc(1, "initialize", map[string]any{"protocolVersion": "2024-11-05"}),
		notification("notifications/initialized"),
		rpc(2, "ping", nil),
		rpc(3, "tools/list", nil),
		rpc(4, "no/such-method", nil),
		`not json at all`,
	})
	if len(resps) != 5 {
		t.Fatalf("got %d responses, want 5 (notification gets none)", len(resps))
	}
	// initialize
	raw, _ := json.Marshal(resps[0].Result)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &init); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if init.ProtocolVersion != protocolVersion || init.ServerInfo.Name != "adcp-test" {
		t.Errorf("bad initialize result: %+v", init)
	}
	// tools/list exposes all five tools
	raw, _ = json.Marshal(resps[2].Result)
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	want := []string{"run_conformance", "list_scenarios", "run_scenario", "debug_signature", "lifecycle_check"}
	if len(list.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(list.Tools), len(want))
	}
	for i, w := range want {
		if list.Tools[i].Name != w {
			t.Errorf("tool %d = %q, want %q", i, list.Tools[i].Name, w)
		}
		if list.Tools[i].Description == "" || list.Tools[i].InputSchema == nil {
			t.Errorf("tool %q missing description or inputSchema", w)
		}
	}
	// unknown method -> -32601
	if resps[3].Error == nil || resps[3].Error.Code != -32601 {
		t.Errorf("unknown method: got %+v, want -32601", resps[3].Error)
	}
	// parse error -> -32700
	if resps[4].Error == nil || resps[4].Error.Code != -32700 {
		t.Errorf("parse error: got %+v, want -32700", resps[4].Error)
	}
}

func TestCallUnknownTool(t *testing.T) {
	resps := scriptedSession(t, []string{call(1, "no_such_tool", nil)})
	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("unknown tool should be a tool-level error, got %+v", resps[0])
	}
	raw, _ := json.Marshal(resps[0].Result)
	var cr callResult
	_ = json.Unmarshal(raw, &cr)
	if !cr.IsError {
		t.Error("unknown tool result should have isError=true")
	}
}

func TestCallMissingArgs(t *testing.T) {
	resps := scriptedSession(t, []string{call(1, "run_conformance", map[string]any{})})
	raw, _ := json.Marshal(resps[0].Result)
	var cr callResult
	_ = json.Unmarshal(raw, &cr)
	if !cr.IsError || !strings.Contains(cr.Content[0].Text, "target is required") {
		t.Errorf("missing target should be a tool error, got %+v", cr)
	}
}

// startExampleSeller loads the worked-example mock seller on an ephemeral
// port for tool integration tests.
func startExampleSeller(t *testing.T) string {
	t.Helper()
	cfg, err := mockcfg.LoadFile("../../examples/mock-seller.yaml")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	svcs := cfg.Services()
	if len(svcs) == 0 {
		t.Fatal("example config has no services")
	}
	svc := svcs[0]
	svc.Listen = ":0"
	srv, err := mockserver.New(svc, mockserver.Options{BaseDir: cfg.BaseDir()})
	if err != nil {
		t.Fatalf("mockserver.New: %v", err)
	}
	url, err := srv.Start()
	if err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return url
}

func TestToolListScenarios(t *testing.T) {
	resps := scriptedSession(t, []string{call(1, "list_scenarios", map[string]any{})})
	text := callText(t, resps[0])
	if !strings.Contains(text, "happy-path-media-buy") {
		t.Errorf("list_scenarios missing built-in pack, got: %s", text)
	}
}

func TestToolRunConformance(t *testing.T) {
	target := startExampleSeller(t)
	resps := scriptedSession(t, []string{call(1, "run_conformance", map[string]any{"target": target})})
	text := callText(t, resps[0])
	var env reportEnvelope
	// The envelope is the text content; decode it back.
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope: %v\ntext: %s", err, text)
	}
	if !env.Passed {
		t.Errorf("conformance against example seller should pass: %s", env.Summary)
	}
	if !strings.Contains(env.Summary, "conformance:") {
		t.Errorf("summary missing headline, got %q", env.Summary)
	}
}

func TestToolRunScenario(t *testing.T) {
	target := startExampleSeller(t)
	resps := scriptedSession(t, []string{call(1, "run_scenario",
		map[string]any{"pack": "happy-path-media-buy", "target": target})})
	text := callText(t, resps[0])
	var env reportEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope: %v\ntext: %s", err, text)
	}
	if !env.Passed {
		t.Errorf("scenario pack against example seller should pass: %s", env.Summary)
	}
}

func TestToolLifecycleCheck(t *testing.T) {
	target := startExampleSeller(t)
	resps := scriptedSession(t, []string{call(1, "lifecycle_check", map[string]any{"target": target})})
	text := callText(t, resps[0])
	var env reportEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope: %v\ntext: %s", err, text)
	}
	if !env.Passed {
		t.Errorf("lifecycle against example seller should pass: %s", env.Summary)
	}
}

func TestToolDebugSignature(t *testing.T) {
	// Unsigned request: the tool must report invalid, not error.
	req := map[string]any{
		"method":  "POST",
		"url":     "https://seller.example/mcp",
		"headers": map[string]string{"content-type": "application/json"},
		"body":    `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
	}
	resps := scriptedSession(t, []string{call(1, "debug_signature",
		map[string]any{"request": req, "key_pem": "not-a-key"})})
	text := callText(t, resps[0])
	var env reportEnvelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope: %v\ntext: %s", err, text)
	}
	if env.Passed {
		t.Error("unsigned request must not verify")
	}
	if !strings.Contains(env.Summary, "invalid") {
		t.Errorf("summary should say invalid, got %q", env.Summary)
	}
}
