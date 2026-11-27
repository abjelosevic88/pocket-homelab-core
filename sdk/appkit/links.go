package appkit

import (
	"net/http"
	"regexp"
	"strings"
)

// Route is one request another Pocket app may make here with a link (pcx_)
// token (docs/links.md): Methods is space-separated ("PUT DELETE"), Path a
// regexp anchored by the caller, Scope the offer it needs ("" = any linked
// app, e.g. GET /api/v1/auth/me).
type Route struct {
	Methods string
	Path    *regexp.Regexp
	Scope   string
}

// R builds a Route from a pattern; it panics on a bad pattern, at start.
func R(methods, pattern, scope string) Route {
	return Route{Methods: methods, Path: regexp.MustCompile(pattern), Scope: scope}
}

// LinkAllower is what core.Portable and core.Capabilities provide for their
// own fixed routes.
type LinkAllower interface {
	LinkAllows(scopes []string, r *http.Request) bool
}

// Routes is an app's link-route allowlist. Every route not listed stays
// closed to link tokens (403).
type Routes []Route

// Allows reports whether a link token with these scopes may make r.
func (rs Routes) Allows(scopes []string, r *http.Request) bool {
	for _, lr := range rs {
		if !strings.Contains(" "+lr.Methods+" ", " "+r.Method+" ") || !lr.Path.MatchString(r.URL.Path) {
			continue
		}
		if lr.Scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.Scope {
				return true
			}
		}
	}
	return false
}

// Allow is Auth.Allow for these routes plus the routes of contracts such as
// core.Portable and core.Capabilities (nil ones are skipped).
func (rs Routes) Allow(contracts ...LinkAllower) func(scopes []string, r *http.Request) bool {
	return func(scopes []string, r *http.Request) bool {
		for _, c := range contracts {
			if c != nil && !isNil(c) && c.LinkAllows(scopes, r) {
				return true
			}
		}
		return rs.Allows(scopes, r)
	}
}

// LinkReason turns the reason Core gives for refusing ActAs (LinkError.Reason)
// into a sentence for the person: self is this app's short name ("Still"),
// target the app it tried to reach ("Pocket Habits"). "" for an unknown
// reason, so the caller can fall back to Core's own message.
func LinkReason(self, target, reason string) string {
	switch reason {
	case "off_person":
		return "you switched the " + self + " → " + target + " link off in Pocket Core (Settings → Apps acting for me)"
	case "off_admin":
		return "the " + self + " → " + target + " link is switched off in Pocket Core (Links)"
	case "target_missing":
		return target + " is not registered with Pocket Core"
	case "not_offered":
		return "this " + target + " version does not accept app links yet"
	case "not_declared":
		return "Pocket Core has not seen this " + self + " version yet; it re-registers within a minute of starting"
	case "not_in_audience":
		return "you do not have access to " + target + " in Pocket Core"
	case "unknown_user":
		return "your email is not a person in Pocket Core"
	}
	return ""
}
