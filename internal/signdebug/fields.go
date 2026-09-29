package signdebug

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Minimal RFC 9421 structured-fields parsing: dictionaries whose values
// are inner lists (Signature-Input) or byte sequences (Signature).

// innerItem is one covered component inside a signature inner list.
type innerItem struct {
	Value  string
	Params map[string]string
	Raw    string // the item exactly as serialized, e.g. `"@query-param";name="lang"`
}

// dictMember is one dictionary member: key, raw value, and — when the
// value is an inner list — its items and trailing parameters.
type dictMember struct {
	Key       string
	Value     string // raw value text (byte sequence or token)
	InnerList []innerItem
	Params    map[string]string
	RawValue  string // the value exactly as serialized (inner-list text)
}

// parseDict parses `k1=v1, k2=(...);p=1` into members.
func parseDict(s string) ([]dictMember, error) {
	var out []dictMember
	for _, part := range splitTopLevel(s, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, found := splitKeyValue(part)
		if !found {
			return nil, fmt.Errorf("malformed dictionary member %q", part)
		}
		m := dictMember{Key: strings.TrimSpace(key), Params: map[string]string{}}
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, "(") {
			items, rest, err := parseInnerList(val)
			if err != nil {
				return nil, fmt.Errorf("member %q: %w", m.Key, err)
			}
			m.InnerList = items
			m.Params = parseParams(rest)
			m.RawValue = val
		} else {
			m.Value = val
			m.RawValue = val
		}
		out = append(out, m)
	}
	return out, nil
}

// parseInnerList parses `("a";p=1 "b");created=123` into items plus the
// trailing parameter text (starting with ';' or empty).
func parseInnerList(s string) ([]innerItem, string, error) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", fmt.Errorf("inner list must start with '('")
	}
	end := matchParen(s)
	if end < 0 {
		return nil, "", fmt.Errorf("unclosed inner list")
	}
	inner := s[1:end]
	rest := s[end+1:]
	var items []innerItem
	for _, tok := range splitSpaces(inner) {
		if tok == "" {
			continue
		}
		val, params, err := parseItem(tok)
		if err != nil {
			return nil, "", err
		}
		items = append(items, innerItem{Value: val, Params: params, Raw: tok})
	}
	return items, rest, nil
}

// parseItem parses `"value";p1=v1;p2="v2"` into the value and its params.
func parseItem(tok string) (string, map[string]string, error) {
	// Split off parameters: first ';' outside quotes.
	head, paramText := splitParamStart(tok)
	head = strings.TrimSpace(head)
	var val string
	if strings.HasPrefix(head, "\"") {
		u, err := unquoteSF(head)
		if err != nil {
			return "", nil, fmt.Errorf("bad string item %q: %w", head, err)
		}
		val = u
	} else {
		val = head // token
	}
	return val, parseParams(paramText), nil
}

// parseParams parses `;created=123;keyid="x";fresh=?1` into a map.
// Values keep their serialized form; callers unquote as needed.
func parseParams(s string) map[string]string {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		if !strings.HasPrefix(s, ";") {
			break
		}
		s = s[1:]
		// name up to '=' or ';'
		i := 0
		for i < len(s) && s[i] != '=' && s[i] != ';' {
			i++
		}
		name := strings.TrimSpace(s[:i])
		s = s[i:]
		if !strings.HasPrefix(s, "=") {
			if name != "" {
				out[name] = ""
			}
			continue
		}
		s = s[1:]
		var val string
		if strings.HasPrefix(s, "\"") {
			end := quotedEnd(s)
			if end < 0 {
				val = s
				s = ""
			} else {
				val = s[:end+1]
				s = s[end+1:]
			}
		} else {
			j := 0
			for j < len(s) && s[j] != ';' {
				j++
			}
			val = strings.TrimSpace(s[:j])
			s = s[j:]
		}
		if name != "" {
			out[name] = val
		}
	}
	return out
}

// unquote strips one layer of DQUOTE unescaping.
func unquote(v string) string {
	u, err := unquoteSF(v)
	if err != nil {
		return v
	}
	return u
}

func unquoteSF(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted string: %q", s)
	}
	var b strings.Builder
	inner := s[1 : len(s)-1]
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' {
			i++
			if i >= len(inner) {
				return "", fmt.Errorf("trailing backslash in %q", s)
			}
			b.WriteByte(inner[i])
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String(), nil
}

// parseByteSequence decodes `:base64:` into bytes.
func parseByteSequence(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != ':' || s[len(s)-1] != ':' {
		return nil, fmt.Errorf("%q is not a byte sequence", s)
	}
	return base64.StdEncoding.DecodeString(s[1 : len(s)-1])
}

// splitTopLevel splits on sep characters that are outside quotes and
// parentheses.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth := 0
	inQuotes := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuotes && c == '\\':
			i++
		case c == '"':
			inQuotes = !inQuotes
		case !inQuotes && c == '(':
			depth++
		case !inQuotes && c == ')':
			depth--
		case !inQuotes && depth == 0 && c == sep:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// splitSpaces splits on ASCII spaces/tabs outside quotes.
func splitSpaces(s string) []string {
	var parts []string
	inQuotes := false
	start := -1
	flush := func(end int) {
		if start >= 0 {
			parts = append(parts, s[start:end])
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuotes && c == '\\' {
			i++
			continue
		}
		if c == '"' {
			inQuotes = !inQuotes
		}
		if !inQuotes && (c == ' ' || c == '\t') {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(s))
	return parts
}

// matchParen returns the index of the ')' matching s[0] == '(',
// respecting quotes; -1 when unclosed.
func matchParen(s string) int {
	depth := 0
	inQuotes := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuotes && c == '\\' {
			i++
			continue
		}
		switch c {
		case '"':
			inQuotes = !inQuotes
		case '(':
			if !inQuotes {
				depth++
			}
		case ')':
			if !inQuotes {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

// splitKeyValue splits on the first '=' outside quotes.
func splitKeyValue(s string) (string, string, bool) {
	inQuotes := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuotes && c == '\\' {
			i++
			continue
		}
		if c == '"' {
			inQuotes = !inQuotes
		}
		if !inQuotes && c == '=' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// splitParamStart splits an item into head and parameter text at the
// first ';' outside quotes.
func splitParamStart(s string) (string, string) {
	inQuotes := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuotes && c == '\\' {
			i++
			continue
		}
		if c == '"' {
			inQuotes = !inQuotes
		}
		if !inQuotes && c == ';' {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// quotedEnd returns the index of the closing quote of a string starting
// at s[0] == '"'; -1 when unterminated.
func quotedEnd(s string) int {
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			return i
		}
	}
	return -1
}
