package signdebug

import (
	"fmt"
	"net/url"
	"strings"
)

// buildComponents reconstructs the value of every covered component from
// the request as received.
//
// headers maps a lowercased field name to its values in message order
// (one entry per field instance). The ;bs parameter needs the ordered
// set of field values, not a pre-combined string — see RFC 9421 §2.1.
func buildComponents(req *Request, headers map[string][]string, covered []innerItem, body []byte) ([]Component, error) {
	var u *url.URL
	if req.URL != "" {
		var err error
		u, err = url.Parse(req.URL)
		if err != nil {
			return nil, fmt.Errorf("request url does not parse: %w", err)
		}
	}
	var out []Component
	for _, it := range covered {
		val, note, err := componentValue(it, req, u, headers, body)
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", it.Value, err)
		}
		out = append(out, Component{
			Name:   it.Value,
			Params: paramsSummary(it.Params),
			Value:  val,
			Note:   note,
		})
	}
	return out, nil
}

func paramsSummary(p map[string]string) string {
	if len(p) == 0 {
		return ""
	}
	var parts []string
	for k, v := range p {
		if v == "" {
			parts = append(parts, k)
		} else {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ";")
}

func componentValue(it innerItem, req *Request, u *url.URL, headers map[string][]string, body []byte) (string, string, error) {
	name := strings.ToLower(it.Value)
	needURL := func() (*url.URL, error) {
		if u == nil {
			return nil, fmt.Errorf("needs a request URL, none was provided")
		}
		return u, nil
	}
	switch name {
	case "@method":
		if strings.TrimSpace(req.Method) == "" {
			return "", "", fmt.Errorf("no request method provided")
		}
		// RFC 9421 §2.2.1: no transformation to the method's case is
		// performed. (An earlier version of this debugger uppercased
		// the method; that breaks interop with signers that sign the
		// method verbatim.)
		return req.Method, "request method, verbatim", nil
	case "@target-uri":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		return targetURI(uu), "rebuilt from the request URL", nil
	case "@authority":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		return strings.ToLower(uu.Host), "lowercased host[:port] from the request URL", nil
	case "@scheme":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		return strings.ToLower(uu.Scheme), "lowercased URL scheme", nil
	case "@path":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		p := uu.EscapedPath()
		if p == "" {
			p = "/"
		}
		return p, "URL path", nil
	case "@query":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		if uu.RawQuery == "" {
			// RFC 9421 §2.2.7: when the query string is absent, the
			// component value is the leading "?" character alone.
			return "?", "no query string in the URL (RFC 9421 §2.2.7: value is \"?\")", nil
		}
		return "?" + uu.RawQuery, "raw query string", nil
	case "@query-param":
		uu, err := needURL()
		if err != nil {
			return "", "", err
		}
		pname := unquote(it.Params["name"])
		if pname == "" {
			return "", "", fmt.Errorf("@query-param needs a ;name parameter")
		}
		val, err := queryParamValue(uu.RawQuery, pname)
		if err != nil {
			return "", "", err
		}
		return val, fmt.Sprintf("encoded value of query parameter %q", pname), nil
	case "@status", "@request-response":
		return "", "", fmt.Errorf("%s is a response component and cannot be verified from a request", name)
	case "content-digest":
		vals, ok := headers["content-digest"]
		if !ok || len(vals) == 0 {
			return "", "", fmt.Errorf("covered but no content-digest header in the request")
		}
		return strings.TrimSpace(vals[0]), "header value, verbatim", nil
	default:
		vals, ok := headers[name]
		if !ok || len(vals) == 0 {
			return "", "", fmt.Errorf("header %q not present in the request", it.Value)
		}
		// Dictionary member selection (;key="member").
		if key, hasKey := it.Params["key"]; hasKey {
			member := unquote(key)
			combined := strings.Join(vals, ", ")
			mv, found := dictMemberValue(combined, member)
			if !found {
				return "", "", fmt.Errorf("header %q has no dictionary member %q", it.Value, member)
			}
			vals = []string{mv}
		}
		if _, hasBS := it.Params["bs"]; hasBS {
			return bsComponentValue(vals)
		}
		// RFC 9421 §2.1 (no ;bs): strip leading/trailing whitespace,
		// replace obs-fold with a single space, join multiple field
		// instances with ", ". Internal whitespace is NOT collapsed.
		cleaned := make([]string, 0, len(vals))
		for _, v := range vals {
			cleaned = append(cleaned, unfoldFieldValue(v))
		}
		return strings.Join(cleaned, ", "), "header value(s), whitespace-normalized per RFC 9421 §2.1", nil
	}
}

// bsComponentValue implements the RFC 9421 §2.1 ;bs algorithm: each field
// value is stripped of leading/trailing whitespace, has obs-fold replaced
// with a single space, is encoded as a Byte Sequence, and the resulting
// values are serialized as a strict structured-fields List
// (":base64:, :base64:"). This keeps multi-instance fields distinct —
// signing them as one combined string would collide semantically
// different messages.
func bsComponentValue(vals []string) (string, string, error) {
	seqs := make([]string, 0, len(vals))
	for _, v := range vals {
		cleaned := unfoldFieldValue(v)
		seqs = append(seqs, ":"+base64Std(cleaned)+":")
	}
	return strings.Join(seqs, ", "),
		"field value(s) encoded as byte sequence(s) (;bs), RFC 9421 §2.1", nil
}

// unfoldFieldValue strips leading/trailing whitespace (OWS) and replaces
// obsolete line folding (CRLF followed by SP/HTAB) with a single space,
// per RFC 9421 §2.1. Unlike a full whitespace collapse, interior spacing
// is preserved: "a  b" stays "a  b".
func unfoldFieldValue(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\r' && i+1 < len(v) && v[i+1] == '\n' {
			// obs-fold: CRLF followed by whitespace folds to one space.
			j := i + 2
			for j < len(v) && (v[j] == ' ' || v[j] == '\t') {
				j++
			}
			b.WriteByte(' ')
			i = j - 1
			continue
		}
		if v[i] == '\n' {
			j := i + 1
			for j < len(v) && (v[j] == ' ' || v[j] == '\t') {
				j++
			}
			b.WriteByte(' ')
			i = j - 1
			continue
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// queryParamValue implements RFC 9421 §2.2.8 @query-param: the query
// string is parsed per application/x-www-form-urlencoded into (encoded
// name, encoded value) tuples; the ;name parameter holds the encoded
// nameString. If the name occurs more than once, the parameter MUST NOT
// be included (error). The returned value is the encoded valueString:
// decode, then re-encode with the "percent-encode after encoding"
// process (space -> %20, not +).
func queryParamValue(rawQuery, encodedName string) (string, error) {
	var matches []string
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		name, value := pair, ""
		if i := strings.IndexByte(pair, '='); i >= 0 {
			name, value = pair[:i], pair[i+1:]
		}
		decodedName, err := formDecode(name)
		if err != nil {
			return "", fmt.Errorf("cannot decode query parameter name %q: %w", name, err)
		}
		if formEncode(decodedName) != encodedName {
			continue
		}
		decodedValue, err := formDecode(value)
		if err != nil {
			return "", fmt.Errorf("cannot decode query parameter %q value: %w", encodedName, err)
		}
		matches = append(matches, formEncode(decodedValue))
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("query parameter %q not present in the URL", encodedName)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("query parameter %q occurs %d times; RFC 9421 §2.2.8 forbids covering a repeated parameter (sign @query instead)", encodedName, len(matches))
	}
	return matches[0], nil
}

// formDecode decodes one application/x-www-form-urlencoded component:
// '+' becomes space, then percent-decoding.
func formDecode(s string) (string, error) {
	s = strings.ReplaceAll(s, "+", " ")
	return url.QueryUnescape(s)
}

// formEncode applies the WHATWG "percent-encode after encoding" process
// used for @query-param values: ASCII alphanumeric and "* - . _" are
// kept, everything else is %XX (uppercase hex). Note space becomes
// %20, not +.
func formEncode(s string) string {
	const keep = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789*-._"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// targetURI rebuilds scheme://authority + path + query.
func targetURI(u *url.URL) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(u.Scheme))
	b.WriteString("://")
	b.WriteString(strings.ToLower(u.Host))
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	b.WriteString(p)
	if u.RawQuery != "" {
		b.WriteString("?")
		b.WriteString(u.RawQuery)
	}
	return b.String()
}

// dictMemberValue extracts member `name=value` from a dictionary-style
// header value (top-level comma split).
func dictMemberValue(header, member string) (string, bool) {
	for _, part := range splitTopLevel(header, ',') {
		part = strings.TrimSpace(part)
		k, v, found := splitKeyValue(part)
		if found && strings.TrimSpace(k) == member {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// buildBase serializes the signature base: one `"id": value` line per
// covered component (identifiers in their original serialization),
// then the `"@signature-params"` line with the exact inner-list text
// from the Signature-Input header.
func buildBase(comps []Component, covered []innerItem, rawInnerList string) string {
	var b strings.Builder
	for i, it := range covered {
		name, paramText := splitItemRaw(it.Raw)
		if name == "" {
			name = `"` + it.Value + `"`
		}
		b.WriteString(name)
		b.WriteString(paramText)
		b.WriteString(": ")
		b.WriteString(comps[i].Value)
		b.WriteString("\n")
	}
	b.WriteString(`"@signature-params": `)
	b.WriteString(rawInnerList)
	return b.String()
}

// splitItemRaw splits a serialized component item into its quoted name
// and raw parameter suffix, e.g. `"@query-param";name="lang"` ->
// (`"@query-param"`, `;name="lang"`).
func splitItemRaw(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "\"") {
		end := quotedEnd(raw)
		if end > 0 {
			return raw[:end+1], raw[end+1:]
		}
	}
	if i := strings.Index(raw, ";"); i >= 0 {
		return `"` + strings.TrimSpace(raw[:i]) + `"`, raw[i:]
	}
	return `"` + raw + `"`, ""
}
