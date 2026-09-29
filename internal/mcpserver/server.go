// Package mcpserver exposes adcp-test as MCP tools over stdio.
//
// It implements the minimal JSON-RPC 2.0 subset an MCP client needs:
// initialize, notifications/initialized, tools/list, tools/call, and
// ping. Messages are newline-delimited JSON on stdin/stdout; all
// logging goes to stderr so stdout stays a clean RPC channel.
//
// This is the dogfood surface: an AI agent (or any MCP client) can drive
// the conformance suite, scenario packs, the RFC 9421 signing debugger,
// and the lifecycle checks without shelling out to the CLI.
//
// Security notes: bearer tokens and PEM keys passed as tool arguments are
// held in memory only and never written to disk by this package, but the
// MCP host itself may log tool calls — treat key material accordingly.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"
)

// protocolVersion is the MCP protocol version this server speaks.
const protocolVersion = "2024-11-05"

// serverVersion tracks the adcp-test release this server ships with.
const serverVersion = "0.3.0"

// maxToolTimeout bounds any single tools/call invocation.
const maxToolTimeout = 10 * time.Minute

// request is one incoming JSON-RPC 2.0 message.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// response is one outgoing JSON-RPC 2.0 message.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// contentItem is one element of a tools/call result content array.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// callResult is the MCP tools/call result envelope.
type callResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// toolHandler runs one tool: it decodes raw (the arguments object) and
// returns the result payload. A non-nil error becomes a tool-level error
// result (isError), not a JSON-RPC error.
type toolHandler func(ctx context.Context, raw json.RawMessage) (any, error)

// tool describes one MCP tool.
type tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     toolHandler
}

// Server serves the MCP tool surface over newline-delimited JSON-RPC 2.0.
type Server struct {
	// In is the request stream (stdin). Out is the response stream
	// (stdout). Log receives server diagnostics; nil discards them.
	In  io.Reader
	Out io.Writer
	Log *log.Logger

	enc *json.Encoder
}

// Serve runs the message loop until ctx is done or the input closes.
func (s *Server) Serve(ctx context.Context) error {
	s.enc = json.NewEncoder(s.Out)
	s.enc.SetEscapeHTML(false)
	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return fmt.Errorf("mcpserver: read: %w", err)
			}
			return nil // clean EOF
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		s.handleLine(ctx, line)
	}
}

// handleLine decodes one message and dispatches it. Notifications (no id)
// never get a response.
func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.write(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		if req.ID != nil {
			s.write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}})
		}
		return
	}
	notification := req.ID == nil
	switch req.Method {
	case "initialize":
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo":      map[string]any{"name": "adcp-test", "version": serverVersion},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}})
	case "notifications/initialized":
		// No-op; notifications get no response.
	case "ping":
		if !notification {
			s.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		}
	case "tools/list":
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": listTools()}})
	case "tools/call":
		if notification {
			return
		}
		s.write(response{JSONRPC: "2.0", ID: req.ID, Result: s.callTool(ctx, req.Params)})
	default:
		if !notification {
			s.write(response{JSONRPC: "2.0", ID: req.ID,
				Error: &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}})
		}
	}
}

// write emits one response; write errors are logged, never fatal to the loop.
func (s *Server) write(resp response) {
	if err := s.enc.Encode(resp); err != nil && s.Log != nil {
		s.Log.Printf("mcpserver: write: %v", err)
	}
}

// callParams are the tools/call parameters.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool dispatches a tools/call to the named tool handler.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) callResult {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		return toolError("invalid tools/call params")
	}
	t := toolByName(p.Name)
	if t == nil {
		return toolError(fmt.Sprintf("unknown tool: %s", p.Name))
	}
	tctx, cancel := context.WithTimeout(ctx, maxToolTimeout)
	defer cancel()
	out, err := t.Handler(tctx, p.Arguments)
	if err != nil {
		return toolError(err.Error())
	}
	text, _ := json.MarshalIndent(out, "", "  ")
	return callResult{Content: []contentItem{{Type: "text", Text: string(text)}}}
}

// toolError builds a tool-level error result.
func toolError(msg string) callResult {
	return callResult{Content: []contentItem{{Type: "text", Text: msg}}, IsError: true}
}
