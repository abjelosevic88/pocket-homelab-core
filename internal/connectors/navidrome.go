package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Navidrome (0.58+) connector: accounts.
//
// Navidrome's own API takes a session token in the X-ND-Authorization header,
// got by signing in with a username and password; there are no API keys. The
// box-wide credential is therefore an administrator's USER and PASSWORD, and
// the credential Core makes for a person is their own USER and PASSWORD (what
// Subsonic clients and Pocket Music sign in with).
//
//	POST /auth/login     Test, Provision (admin session)  read (opens a session)
//	GET  /api/user       Test, Provision                  read
//	POST /api/user       Provision, missing person        WRITE
//
// Accounts carry emails, so a person matches the account with their email.
// A new account is a regular user with a random 32-character password and
// the libraries Navidrome gives new users by default. Navidrome has no
// OpenID sign-in and no event feed.
type navidrome struct{}

func init() { Register(navidrome{}) }

func (navidrome) Type() string               { return "navidrome" }
func (navidrome) Capabilities() Capabilities { return Capabilities{Provision: true} }
func (navidrome) SSORedirects(string) []string {
	return nil
}

func (navidrome) ConfigureSSO(_ context.Context, _ Target, sc SSOConfig) error {
	if sc.Enabled {
		return errors.New("Navidrome has no OpenID sign-in")
	}
	return nil
}

func (navidrome) Poll(_ context.Context, _ Target, state json.RawMessage) ([]Event, json.RawMessage, error) {
	return nil, state, nil
}

type ndUser struct {
	ID       string `json:"id"`
	UserName string `json:"userName"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	IsAdmin  bool   `json:"isAdmin"`
}

// session signs in as the box-wide administrator.
func (navidrome) session(ctx context.Context, t Target) (string, error) {
	user, pass := strings.TrimSpace(t.Fields["USER"]), t.Fields["PASSWORD"]
	if user == "" || pass == "" {
		return "", errors.New("the Navidrome service in Core needs USER and PASSWORD of a Navidrome administrator")
	}
	var out struct {
		Token   string `json:"token"`
		IsAdmin bool   `json:"isAdmin"`
	}
	_, err := doJSON(ctx, t, "Navidrome", http.MethodPost, "/auth/login", nil, map[string]string{"username": user, "password": pass}, &out, pass)
	if appStatus(err) == http.StatusUnauthorized {
		return "", errors.New("Navidrome rejected the stored USER and PASSWORD (HTTP 401)")
	}
	if err != nil {
		return "", err
	}
	if !out.IsAdmin {
		return "", errors.New("the stored Navidrome account is not an administrator: Core needs one to make accounts")
	}
	if out.Token == "" {
		return "", errors.New("Navidrome signed in without a session token")
	}
	return out.Token, nil
}

func (navidrome) users(ctx context.Context, t Target, token string) ([]ndUser, error) {
	var list []ndUser
	_, err := doJSON(ctx, t, "Navidrome", http.MethodGet, "/api/user?_start=0&_end=10000", map[string]string{"X-ND-Authorization": "Bearer " + token}, nil, &list, token)
	return list, err
}

func (c navidrome) Test(ctx context.Context, t Target) (Info, error) {
	token, err := c.session(ctx, t)
	if err != nil {
		return Info{}, err
	}
	list, err := c.users(ctx, t, token)
	if err != nil {
		return Info{}, err
	}
	return Info{Admin: true, Users: len(list)}, nil
}

var ndUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// ndUserName picks a free username from the email's local part.
func ndUserName(email string, list []ndUser) string {
	base := strings.ToLower(strings.SplitN(email, "@", 2)[0])
	base = strings.Trim(ndUnsafe.ReplaceAllString(base, "-"), "-.")
	if base == "" {
		base = "user"
	}
	taken := map[string]bool{}
	for _, u := range list {
		taken[strings.ToLower(u.UserName)] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = base + strconv.Itoa(i)
	}
	return name
}

func (c navidrome) Provision(ctx context.Context, t Target, p Person) (Account, error) {
	token, err := c.session(ctx, t)
	if err != nil {
		return Account{}, err
	}
	list, err := c.users(ctx, t, token)
	if err != nil {
		return Account{}, err
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	for _, u := range list {
		if strings.ToLower(strings.TrimSpace(u.Email)) == email && email != "" {
			note := "an account with this email already existed; its password is unknown to Core"
			if u.IsAdmin {
				note = "this email belongs to a Navidrome administrator; Core does not touch it"
			}
			return Account{RemoteID: u.ID, Status: StatusLinked, Note: note}, nil
		}
	}
	if p.KeepOwnCredential {
		return Account{}, errors.New("no Navidrome account has this email, and the person keeps their own credential: create the account in Navidrome by hand")
	}
	name := ndUserName(email, list)
	password := RandomPassword(32)
	display := strings.TrimSpace(p.Name)
	if display == "" {
		display = name
	}
	var made ndUser
	body := map[string]any{"userName": name, "name": display, "email": email, "password": password, "isAdmin": false}
	if _, err := doJSON(ctx, t, "Navidrome", http.MethodPost, "/api/user", map[string]string{"X-ND-Authorization": "Bearer " + token}, body, &made, token, password); err != nil {
		return Account{}, err
	}
	if made.ID == "" {
		return Account{}, errors.New("Navidrome made the account but returned no id")
	}
	return Account{RemoteID: made.ID, Status: StatusOK, Created: true, Fields: map[string]string{"USER": name, "PASSWORD": password}}, nil
}
