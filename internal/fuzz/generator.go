package fuzz

import (
	"fmt"
	"math/rand"
	"strings"
)

// generator produces deterministic pseudo-random payloads from a seed.
type generator struct {
	rng *rand.Rand
	n   int
}

func newGenerator(seed int64) *generator {
	return &generator{rng: rand.New(rand.NewSource(seed))}
}

// next returns the next payload.
func (g *generator) next() []byte {
	g.n++
	switch g.rng.Intn(10) {
	case 0, 1:
		return g.mangledRPC()
	case 2:
		return g.garbage()
	case 3:
		return g.truncated()
	case 4:
		return g.wrongTypes()
	case 5:
		return g.deepNesting()
	case 6:
		return g.huge()
	case 7:
		return g.batch()
	case 8:
		return g.unknownMethod()
	default:
		return g.validButOdd()
	}
}

func (g *generator) pick(ss []string) string { return ss[g.rng.Intn(len(ss))] }

// mangledRPC is almost-valid JSON-RPC with one structural flaw.
func (g *generator) mangledRPC() []byte {
	id := g.rng.Intn(1000)
	methods := []string{"tools/call", "tools/list", "initialize", "ping"}
	m := g.pick(methods)
	switch g.rng.Intn(6) {
	case 0: // missing jsonrpc field
		return []byte(fmt.Sprintf(`{"id":%d,"method":%q,"params":{"x":1}}`, id, m))
	case 1: // jsonrpc wrong version
		return []byte(fmt.Sprintf(`{"jsonrpc":"1.0","id":%d,"method":%q,"params":{}}`, id, m))
	case 2: // id of odd type
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":{"nested":[1,2]},"method":%q,"params":{}}`, m))
	case 3: // params as array for a named-params call
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":[1,2,3]}`, id, m))
	case 4: // extra unknown top-level fields
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{},"zzz":%q}`, id, m, strings.Repeat("z", 64)))
	default: // notification (no id) with garbage method
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":null}`, "\x00\xff"+m))
	}
}

// garbage is not JSON at all.
func (g *generator) garbage() []byte {
	opts := [][]byte{
		[]byte(""),
		[]byte("null"),
		[]byte("hello world"),
		[]byte("\x00\x01\x02\xff\xfe"),
		[]byte(strings.Repeat("{", 500)),
		[]byte(strings.Repeat("[", 500)),
	}
	return opts[g.rng.Intn(len(opts))]
}

// truncated is valid JSON-RPC cut off mid-stream.
func (g *generator) truncated() []byte {
	full := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"x","arguments":{"a":[1,2,3]}}}`, g.rng.Intn(1000))
	cut := 1 + g.rng.Intn(len(full)-1)
	return []byte(full[:cut])
}

// wrongTypes puts JSON values of the wrong type in key positions.
func (g *generator) wrongTypes() []byte {
	opts := []string{
		`{"jsonrpc":2.0,"id":1,"method":"tools/call","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":42,"params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"string-not-object"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":123,"arguments":[]}}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"tools/call","params":{}}`,
		`{"jsonrpc":"2.0","id":null,"method":null,"params":null}`,
	}
	return []byte(g.pick(opts))
}

// deepNesting nests objects/arrays beyond casual recursion limits.
func (g *generator) deepNesting() []byte {
	depth := 50 + g.rng.Intn(500)
	var b strings.Builder
	b.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"d":`)
	for i := 0; i < depth; i++ {
		b.WriteString(`{"n":`)
	}
	b.WriteString(`1`)
	for i := 0; i < depth; i++ {
		b.WriteString(`}`)
	}
	b.WriteString(`}}}`)
	return []byte(b.String())
}

// huge sends an oversized but valid payload.
func (g *generator) huge() []byte {
	size := 64<<10 + g.rng.Intn(256<<10) // 64–320 KiB
	arg := strings.Repeat("A", size)
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"blob":%q}}}`, arg))
}

// batch sends a JSON-RPC batch with mixed valid/invalid members.
func (g *generator) batch() []byte {
	return []byte(`[
		{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"x"}},
		"not-an-object",
		{"jsonrpc":"2.0","id":3,"method":"nope","params":{}},
		{"id":4,"method":"tools/call"}
	]`)
}

// unknownMethod calls methods that do not exist.
func (g *generator) unknownMethod() []byte {
	m := g.pick([]string{
		"tools/delete_everything",
		"admin/shutdown",
		"../escape",
		"",
		strings.Repeat("x", 300),
		"tools/call\x00injected",
	})
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{}}`, g.rng.Intn(1000), m))
}

// validButOdd is well-formed but semantically strange.
func (g *generator) validButOdd() []byte {
	opts := []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"__proto__":{"polluted":true}}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{"a":1e400,"b":-1e400}}}`,
		`{"jsonrpc":"2.0","id":9223372036854775807,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x","arguments":{},"extra":{"deep":{"deeper":{}}}}}`,
	}
	return []byte(g.pick(opts))
}
