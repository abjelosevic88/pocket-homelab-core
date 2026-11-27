package appkit

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// ErrLinkScope is a link token used outside its scopes: answer 403, not 401.
var ErrLinkScope = errors.New("outside the link's scope")

// ErrNoCredentials means the request carried nothing to sign in with.
var ErrNoCredentials = http.ErrNoCookie

// Via is the Pocket app acting for the person when the request came in on a
// link (pcx_) token; zero when the person asked themself.
type Via struct{ Slug, Name string }

// BearerToken is the token in "Authorization: Bearer …" or X-API-Key.
func BearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.Header.Get("X-API-Key")
}

// Auth resolves who a request is for, in the order every Pocket app uses:
//
//  1. a bearer token: a Pocket Core link token (pcx_, checked against Allow),
//     a Core person/device/OIDC-client token (pcd_/pcu_/pco_), else one of
//     the app's own API tokens;
//  2. Fallback, when set and there was no bearer token (Basic auth for
//     Nextcloud-style clients, a share link…);
//  3. by Mode: "none" → the box's single person, "proxy" → the trusted
//     header, anything else → the session cookie, checked with Core when the
//     session came from it.
//
// U is the app's user type (usually *store.User). Core, APIToken, Session
// and Person are required; Identity and Allow are needed for Core tokens.
type Auth[U any] struct {
	Core *core.Client
	// Slug is the app's own slug; when set, a link token must be for it
	// (Core already refuses one meant for another app; this checks again).
	Slug string
	// Mode is AUTH_MODE: "local" (default), "proxy" or "none".
	Mode func() string
	// ProxyHeader / ProxyEmailHeader name the trusted headers in proxy mode.
	ProxyHeader, ProxyEmailHeader string
	// SessionCookie is the session cookie's name.
	SessionCookie string

	// APIToken looks up one of the app's own tokens.
	APIToken func(ctx context.Context, tok string) (U, error)
	// Session looks up a session id; Core then checks Core sessions.
	Session func(ctx context.Context, id string) (U, error)
	// Person finds or creates the account for an email (no-auth and proxy
	// modes): the app's firstOrSystemUser.
	Person func(ctx context.Context, email, name string) (U, error)
	// Identity maps an active Core introspection to the local account (the
	// app's coreIdentityUser, usually core.ResolveIdentity underneath).
	Identity func(ctx context.Context, in *core.Introspection) (U, error)
	// Allow says whether a link token with these scopes may make this request
	// (see Routes). Nil closes every route to link tokens.
	Allow func(scopes []string, r *http.Request) bool
	// Fallback runs when there is no bearer token; ok=false moves on to the
	// mode/session step.
	Fallback func(r *http.Request) (u U, ok bool, err error)
}

// Resolve answers the person and, for a link token, the acting app. err is
// ErrLinkScope for a link token outside its scopes.
func (a *Auth[U]) Resolve(r *http.Request) (U, Via, error) {
	var zero U
	ctx := r.Context()
	if tok := BearerToken(r); tok != "" {
		if a.Core.Configured() && core.IsLinkToken(tok) {
			return a.link(ctx, tok, r)
		}
		if a.Core.Configured() && core.IsCoreToken(tok) {
			u, err := a.CoreToken(ctx, tok)
			return u, Via{}, err
		}
		u, err := a.APIToken(ctx, tok)
		return u, Via{}, err
	}
	if a.Fallback != nil {
		if u, ok, err := a.Fallback(r); ok || err != nil {
			return u, Via{}, err
		}
	}
	mode := "local"
	if a.Mode != nil {
		mode = a.Mode()
	}
	switch mode {
	case "none":
		u, err := a.Person(ctx, "admin@localhost", "Admin")
		return u, Via{}, err
	case "proxy":
		name := r.Header.Get(firstNonEmpty(a.ProxyHeader, "Remote-User"))
		if name == "" {
			return zero, Via{}, ErrNoCredentials
		}
		email := r.Header.Get(firstNonEmpty(a.ProxyEmailHeader, "Remote-Email"))
		if email == "" {
			email = name
			if !strings.Contains(email, "@") {
				email += "@proxy"
			}
		}
		u, err := a.Person(ctx, strings.ToLower(email), name)
		return u, Via{}, err
	}
	c, err := r.Cookie(a.SessionCookie)
	if err != nil || c.Value == "" {
		return zero, Via{}, ErrNoCredentials
	}
	u, err := a.Session(ctx, c.Value)
	if err != nil {
		return zero, Via{}, err
	}
	if err := a.validate(ctx, c.Value); err != nil {
		return zero, Via{}, err
	}
	return u, Via{}, nil
}

// CoreToken validates a token Core issued to a paired phone (pcd_), a
// person (pcu_) or an OIDC client (pco_) and maps it to the local account.
// Link tokens are refused here: they must go through Resolve's route check.
func (a *Auth[U]) CoreToken(ctx context.Context, tok string) (U, error) {
	var zero U
	if core.IsLinkToken(tok) || a.Identity == nil {
		return zero, ErrNoCredentials
	}
	in, err := a.Core.Introspect(ctx, tok)
	if err != nil {
		return zero, err
	}
	if !in.Active || in.Delegated() || in.User == nil || in.User.Email == "" {
		return zero, ErrNoCredentials
	}
	return a.Identity(ctx, in)
}

// link validates a pcx_ token, checks the route against its scopes and maps
// it to the person it acts for.
func (a *Auth[U]) link(ctx context.Context, tok string, r *http.Request) (U, Via, error) {
	var zero U
	if a.Identity == nil {
		return zero, Via{}, ErrNoCredentials
	}
	in, err := a.Core.Introspect(ctx, tok)
	if err != nil {
		return zero, Via{}, err
	}
	if !in.Active || !in.Delegated() || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return zero, Via{}, ErrNoCredentials
	}
	if a.Slug != "" && in.Audience != a.Slug {
		return zero, Via{}, ErrNoCredentials
	}
	if a.Allow == nil || !a.Allow(strings.Fields(in.Scope), r) {
		return zero, Via{}, ErrLinkScope
	}
	u, err := a.Identity(ctx, in)
	if err != nil {
		return zero, Via{}, err
	}
	return u, Via{Slug: in.App.Slug, Name: in.App.Name}, nil
}

// Require is middleware: it resolves the request, answers 403 for a link
// token outside its scopes and 401 for anything else that fails, and hands
// the person (and the acting app) to put, which returns the request the
// handler runs with.
func (a *Auth[U]) Require(put func(r *http.Request, u U, via Via) *http.Request) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, via, err := a.Resolve(r)
			if errors.Is(err, ErrLinkScope) {
				WriteErr(w, http.StatusForbidden, "this Pocket Core link does not cover "+r.Method+" "+r.URL.Path)
				return
			}
			if err != nil || isNil(u) {
				WriteErr(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, put(r, u, via))
		})
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

// validate checks a session that came from Core with Core (revocation, loss
// of audience); password sessions stay local.
func (a *Auth[U]) validate(ctx context.Context, session string) error {
	if a.Core == nil {
		return nil
	}
	return a.Core.ValidateSession(ctx, session)
}
