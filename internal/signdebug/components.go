package signdebug

import (
	"fmt"
	"net/url"
	"strings"
)

// buildComponents reconstructs the value of every covered component from
// the request as received.
func buildComponents(req *Request, headers map[string]string, covered []innerItem, body []byte) ([]Component, error) {
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

func componentValue(it innerItem, req *Request, u *url.URL, headers map[string]string, body []byte) (string, string, error) {
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
		return strings.ToUpper(req.Method), "uppercased request method", nil
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
			return "", "no query string in the URL", nil
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
		vals := uu.Query()[pname]
		if len(vals) == 0 {
			return "", "", fmt.Errorf("query parameter %q not present in the URL", pname)
		}
		return vals[0], fmt.Sprintf("first value of query parameter %q", pname), nil
	case "@status", "@request-response":
		return "", "", fmt.Errorf("%s is a response component and cannot be verified from a request", name)
	case "content-digest":
		v, ok := headers["content-digest"]
		if !ok {
			return "", "", fmt.Errorf("covered but no content-digest header in the request")
		}
		return strings.TrimSpace(v), "header value, verbatim", nil
	default:
		v, ok := headers[name]
		if !ok {
			return "", "", fmt.Errorf("header %q not present in the request", it.Value)
		}
		// Dictionary member selection (;key="member").
		if key, hasKey := it.Params["key"]; hasKey {
			member := unquote(key)
			mv, found := dictMemberValue(v, member)
			if !found {
				return "", "", fmt.Errorf("header %q has no dictionary member %q", it.Value, member)
			}
			v = mv
		}
		v = unfoldHeader(v)
		note := "header value, whitespace-normalized"
		if _, hasBS := it.Params["bs"]; hasBS {
			v = ":" + base64Std(v) + ":"
			note = "header value encoded as a byte sequence (;bs)"
		}
		return v, note, nil
	}
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

// unfoldHeader replaces obs-fold (CRLF + whitespace) with a single
// space and trims surrounding whitespace, per RFC 9421 §2.5.
func unfoldHeader(v string) string {
	v = strings.ReplaceAll(v, "\r\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	fields := strings.Fields(v)
	return strings.Join(fields, " ")
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
