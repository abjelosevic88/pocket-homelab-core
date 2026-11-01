package installer

import (
	"fmt"
	"strings"
)

// Caddyfile editing is plain text: sites are top-level blocks whose first
// line lists their addresses and ends in "{". Everything else in the file is
// left byte for byte.

// siteHost reduces a site address to its host: "https://a.b:443" → "a.b".
func siteHost(addr string) string {
	addr = strings.TrimSpace(addr)
	if _, rest, ok := strings.Cut(addr, "://"); ok {
		addr = rest
	}
	addr, _, _ = strings.Cut(addr, "/")
	if h, _, ok := strings.Cut(addr, ":"); ok {
		addr = h
	}
	return strings.ToLower(addr)
}

// siteAddrs splits a top-level block opener into its addresses; nil when the
// line opens no site (snippets, global options, directives).
func siteAddrs(line string) []string {
	if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' || line[0] == '(' || !strings.HasSuffix(strings.TrimSpace(line), "{") {
		return nil
	}
	head := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "{"))
	if head == "" {
		return nil
	}
	return strings.FieldsFunc(head, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// HasSite reports whether any site block serves host.
func HasSite(caddyfile, host string) bool {
	for _, line := range strings.Split(caddyfile, "\n") {
		for _, a := range siteAddrs(line) {
			if siteHost(a) == host {
				return true
			}
		}
	}
	return false
}

// AddSite appends a site that proxies host to upstream.
func AddSite(caddyfile, comment, host string, imports []string, upstream string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(caddyfile, "\n"))
	b.WriteString("\n\n")
	if comment != "" {
		b.WriteString("# " + comment + "\n")
	}
	b.WriteString(host + " {\n")
	for _, imp := range imports {
		b.WriteString("\timport " + imp + "\n")
	}
	b.WriteString("\treverse_proxy " + upstream + " {\n\t\tflush_interval -1\n\t}\n}\n")
	return b.String()
}

// RemoveSite drops host from the file. A block that serves only host goes
// with the comment lines right above it; a block shared with other addresses
// only loses host from its address list. It reports what it did.
func RemoveSite(caddyfile, host string) (string, string, error) {
	lines := strings.Split(caddyfile, "\n")
	for i, line := range lines {
		addrs := siteAddrs(line)
		var keep []string
		found := false
		for _, a := range addrs {
			if siteHost(a) == host {
				found = true
			} else {
				keep = append(keep, a)
			}
		}
		if !found {
			continue
		}
		if len(keep) > 0 {
			lines[i] = strings.Join(keep, ", ") + " {"
			return strings.Join(lines, "\n"), fmt.Sprintf("removed %s from the site it shared with %s", host, strings.Join(keep, ", ")), nil
		}
		end, depth := -1, 0
		for j := i; j < len(lines); j++ {
			depth += braceDelta(lines[j])
			if depth == 0 {
				end = j
				break
			}
		}
		if end < 0 {
			return caddyfile, "", fmt.Errorf("the %s block in the Caddyfile never closes; left it alone", host)
		}
		start := i
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") {
			start--
		}
		// one blank line goes with the block, so removals leave no gaps
		if end+1 < len(lines) && strings.TrimSpace(lines[end+1]) == "" && (start == 0 || strings.TrimSpace(lines[start-1]) == "") {
			end++
		}
		out := append(append([]string{}, lines[:start]...), lines[end+1:]...)
		return strings.Join(out, "\n"), fmt.Sprintf("removed the %s site", host), nil
	}
	return caddyfile, "", nil
}

// braceDelta counts block braces on a line, ignoring comments, quoted text
// and placeholders such as {env.X} or {args[0]} (they open and close on the
// same line, so they cancel out anyway).
func braceDelta(line string) int {
	d, quoted := 0, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\':
			i++
		case c == '"':
			quoted = !quoted
		case quoted:
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return d
		case c == '{':
			d++
		case c == '}':
			d--
		}
	}
	return d
}
