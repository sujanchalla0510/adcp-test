package mockserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// templateContext supplies the values for {{...}} placeholders in a
// template response file.
type templateContext struct {
	Tool     string
	ID       json.RawMessage
	Args     map[string]any
	Call     uint64
	Now      time.Time
	State    string
	EntityID string
}

// renderTemplate substitutes {{...}} placeholders. Placeholders:
//
//	{{args.path.to.field}}  request argument (dotted path)
//	{{tool}}                tool name
//	{{id}}                  JSON-RPC request id (raw JSON)
//	{{now}}                 current UTC time, RFC3339
//	{{call}}                1-based call count for this route
//	{{state}}               lifecycle state (state-machine routes)
//	{{entity_id}}           lifecycle entity id (state-machine routes)
//
// String values are JSON-escaped for embedding inside quoted template
// text ("id": "{{args.id}}"); non-strings are embedded as compact JSON
// ("count": {{args.count}}).
func renderTemplate(tmpl string, ctx templateContext) (string, error) {
	var b strings.Builder
	rest := tmpl
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			if strings.Contains(rest, "}}") {
				return "", fmt.Errorf("stray closing delimiter without opening")
			}
			b.WriteString(rest)
			return b.String(), nil
		}
		if strings.Contains(rest[:open], "}}") {
			return "", fmt.Errorf("stray closing delimiter without opening")
		}
		b.WriteString(rest[:open])
		after := rest[open+2:]
		close := strings.Index(after, "}}")
		if close < 0 {
			return "", fmt.Errorf("unclosed placeholder")
		}
		val, err := resolvePlaceholder(strings.TrimSpace(after[:close]), ctx)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		rest = after[close+2:]
	}
}

func resolvePlaceholder(name string, ctx templateContext) (string, error) {
	parts := strings.Split(name, ".")
	switch parts[0] {
	case "tool":
		return jsonRawString(ctx.Tool), nil
	case "id":
		if len(ctx.ID) == 0 {
			return "null", nil
		}
		return string(ctx.ID), nil
	case "now":
		return jsonRawString(ctx.Now.UTC().Format(time.RFC3339)), nil
	case "call":
		return fmt.Sprintf("%d", ctx.Call), nil
	case "state":
		return jsonRawString(ctx.State), nil
	case "entity_id":
		return jsonRawString(ctx.EntityID), nil
	case "args":
		if len(parts) < 2 {
			return "", fmt.Errorf("{{args}} needs a field path, e.g. {{args.buyer_ref}}")
		}
		v, ok := lookupPath(ctx.Args, parts[1:])
		if !ok {
			return "", fmt.Errorf("unknown placeholder path %q", name)
		}
		return jsonEmbed(v)
	default:
		return "", fmt.Errorf("unknown placeholder %q", name)
	}
}

// jsonRawString JSON-escapes s for embedding inside already-quoted
// template text.
func jsonRawString(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(q[1 : len(q)-1])
}

// jsonEmbed renders v for placeholder substitution: strings escaped
// without quotes, everything else as compact JSON.
func jsonEmbed(v any) (string, error) {
	if s, ok := v.(string); ok {
		return jsonRawString(s), nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode placeholder value: %w", err)
	}
	return string(out), nil
}
