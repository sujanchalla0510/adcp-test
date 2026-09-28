package conformance

import (
	"encoding/json"
	"testing"
)

func TestValidateArgs(t *testing.T) {
	full := json.RawMessage(`{"account":"a","brand":"b","start_time":"2026-01-01","end_time":"2026-02-01"}`)
	if p := ValidateArgs("create_media_buy", full); len(p) != 0 {
		t.Fatalf("full args flagged: %v", p)
	}

	partial := json.RawMessage(`{"account":"a"}`)
	problems := ValidateArgs("create_media_buy", partial)
	if len(problems) != 3 {
		t.Fatalf("problems = %v, want 3 missing fields", problems)
	}

	// Unknown tool: nothing to check against.
	if p := ValidateArgs("not_a_tool", partial); len(p) != 0 {
		t.Fatalf("unknown tool flagged: %v", p)
	}

	// Known tool with no expected required fields: no problems.
	if p := ValidateArgs("get_media_buys", json.RawMessage(`{}`)); len(p) != 0 {
		t.Fatalf("no-expectation tool flagged: %v", p)
	}

	// Non-object arguments.
	if p := ValidateArgs("create_media_buy", json.RawMessage(`[1,2]`)); len(p) == 0 {
		t.Fatal("non-object arguments not flagged")
	}

	// Invalid JSON.
	if p := ValidateArgs("create_media_buy", json.RawMessage(`{oops`)); len(p) == 0 {
		t.Fatal("invalid JSON arguments not flagged")
	}

	// get_products requires a brief.
	if p := ValidateArgs("get_products", json.RawMessage(`{}`)); len(p) != 1 {
		t.Fatalf("missing brief not flagged: %v", p)
	}
}
