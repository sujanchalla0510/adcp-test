package conformance

import "encoding/json"

// ValidateArgs checks a tools/call's arguments against the tool's
// expected required fields (from CoreTools). It is the same expectation
// set the conformance suite uses for inputSchema checks, applied here to
// actual call arguments so the Inspect timeline can badge each recorded
// call.
//
// Unknown tools, and tools with no expected required fields, yield no
// problems (there is nothing to check against). An empty return means
// the arguments satisfy the expectations.
func ValidateArgs(tool string, args json.RawMessage) []string {
	exp, ok := expectedToolByName()[tool]
	if !ok || len(exp.ExpectedRequired) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return []string{"arguments is not valid JSON: " + err.Error()}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return []string{"arguments is not a JSON object"}
	}
	var problems []string
	for _, want := range exp.ExpectedRequired {
		if _, present := obj[want]; !present {
			problems = append(problems, "missing required argument "+quote(want))
		}
	}
	return problems
}
