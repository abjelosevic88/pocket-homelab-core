package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Replacing a person's key (KeyRotator), per app. Core only asks for
// accounts its connector created and whose stored key it minted; every
// implementation still checks the account is not an administrator's and only
// deletes keys Core made for that one account.

// OldStillWorks: the check after a replacement found the old key alive.
const OldStillWorks = "the new key is saved, but the old one still works there: delete it in the app by hand"

// ---- Immich ----

// immichKey is a key as Immich lists it.
type immichKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// immichNewKey reads POST /api-keys: {secret, apiKey: {id}} (older builds put id at the top).
type immichNewKey struct {
	Secret string    `json:"secret"`
	ID     string    `json:"id"`
	APIKey immichKey `json:"apiKey"`
}

func (k immichNewKey) id() string {
	if k.APIKey.ID != "" {
		return k.APIKey.ID
	}
	return k.ID
}

// RotateKey mints a new key as the person and deletes the old ones Core
// minted (named ImmichKeyName, and the stored key itself). Immich lets only
// a user mint their own keys, so Core acts with the stored key; only when that
// no longer works does it set a new random password on the account (as admin)
// and sign in as it once.
func (c immich) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	adm, err := adminKey(t)
	if err != nil {
		return out, err
	}
	list, err := c.users(ctx, t, adm)
	if err != nil {
		return out, err
	}
	var user *immichUser
	for i := range list {
		if list[i].ID == a.RemoteID {
			user = &list[i]
		}
	}
	if user == nil {
		return out, fmt.Errorf("Immich has no account %s any more", a.RemoteID)
	}
	if user.IsAdmin {
		return out, fmt.Errorf("the Immich account %s is an admin: Core leaves its keys alone", a.RemoteID)
	}
	old := strings.TrimSpace(a.Fields["API_KEY"])
	var as auth // who mints the new key
	oldID := "" // the stored key's id, when Immich still knows it
	logout := false
	if old != "" {
		oa := auth{apiKey: old}
		var me immichUser
		if err := c.call(ctx, t, http.MethodGet, "/users/me", oa, nil, &me); err == nil {
			if me.ID != a.RemoteID {
				return out, fmt.Errorf("the stored key belongs to another Immich account (%s, not %s): Core leaves it alone", me.ID, a.RemoteID)
			}
			as = oa
			var k immichKey
			if c.call(ctx, t, http.MethodGet, "/api-keys/me", oa, nil, &k) == nil {
				oldID = k.ID
			}
		} else if s := statusOf(err); s != http.StatusUnauthorized && s != http.StatusForbidden {
			return out, err
		}
	}
	var made immichNewKey
	body := map[string]any{"name": ImmichKeyName, "permissions": []string{"all"}}
	if as.apiKey != "" {
		if err := c.call(ctx, t, http.MethodPost, "/api-keys", as, body, &made); err != nil {
			if s := statusOf(err); s != http.StatusBadRequest && s != http.StatusForbidden && s != http.StatusUnauthorized {
				return out, fmt.Errorf("minting a new Immich key with the stored one failed: %w", err)
			}
			as = auth{} // the stored key may not mint keys: sign in instead
		}
	}
	if as.apiKey == "" {
		// The stored key is gone or may not mint: nobody knows this account's
		// password (Core made it), so set a new random one and sign in once.
		password := RandomPassword(40)
		if err := c.call(ctx, t, http.MethodPut, "/admin/users/"+url.PathEscape(a.RemoteID), adm, map[string]any{"password": password}, nil, password); err != nil {
			if statusOf(err) == http.StatusForbidden {
				return out, fmt.Errorf("the stored key no longer works and the admin key may not set the account's password (it needs adminUser.update, or \"all\"): %w", err)
			}
			return out, fmt.Errorf("setting a new password on the Immich account failed: %w", needAdmin(err))
		}
		var login struct {
			AccessToken string `json:"accessToken"`
		}
		if err := c.call(ctx, t, http.MethodPost, "/auth/login", auth{}, map[string]string{"email": user.Email, "password": password}, &login, password); err != nil || login.AccessToken == "" {
			if err == nil {
				err = errors.New("no access token in the answer")
			}
			return out, fmt.Errorf("signing in as the Immich account to mint its new key failed (is password login turned off in Immich?): %w", err)
		}
		as, logout = auth{bearer: login.AccessToken}, true
		out.Note = "the stored key no longer worked, so Core set a new random password on the account to sign in as it"
		if err := c.call(ctx, t, http.MethodPost, "/api-keys", as, body, &made, password); err != nil {
			_ = c.call(ctx, t, http.MethodPost, "/auth/logout", as, nil, nil)
			return out, fmt.Errorf("minting the new Immich key failed: %w", err)
		}
	}
	if logout {
		defer func() { _ = c.call(ctx, t, http.MethodPost, "/auth/logout", as, nil, nil) }()
	}
	if made.Secret == "" {
		return out, errors.New("Immich minted a key but sent no secret")
	}
	out.Fields = map[string]string{"API_KEY": made.Secret}
	// Delete the old keys with the new one: the stored key may be among them.
	na := auth{apiKey: made.Secret}
	var keys []immichKey
	if err := c.call(ctx, t, http.MethodGet, "/api-keys", na, nil, &keys); err != nil {
		return out, fmt.Errorf("the new key is saved but listing the old ones failed, so they still work: %w", err)
	}
	newID := made.id()
	var failed []string
	for _, k := range keys {
		ours := k.ID == oldID && oldID != ""
		if newID != "" && k.Name == ImmichKeyName {
			ours = true // by name only when the new key can be told apart
		}
		if !ours || k.ID == newID {
			continue
		}
		if err := c.call(ctx, t, http.MethodDelete, "/api-keys/"+url.PathEscape(k.ID), na, nil, nil); err != nil && statusOf(err) != http.StatusNotFound {
			failed = append(failed, k.ID+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("the new key is saved but deleting the old one(s) failed, so they still work: %s", strings.Join(failed, "; "))
	}
	if old != "" && c.call(ctx, t, http.MethodGet, "/users/me", auth{apiKey: old}, nil, nil) == nil {
		return out, errors.New(OldStillWorks)
	}
	return out, nil
}

// ---- Jellyfin ----

type jfDevice struct {
	ID         string `json:"Id"`
	AppName    string `json:"AppName"`
	LastUserID string `json:"LastUserId"`
}

// RotateKey signs the account in again from a new Core device and deletes
// Core's older devices of that account, which revokes their tokens. The new
// session comes from Quick Connect, authorized with the stored token (or, if
// that is gone, by the admin key for the account); with Quick Connect off,
// Core sets a new random password and signs in with it, and Jellyfin then
// signs the account out everywhere.
func (c jellyfin) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	list, err := c.users(ctx, t)
	if err != nil {
		return out, err
	}
	var user *jfUser
	for i := range list {
		if jfSameID(list[i].ID, a.RemoteID) {
			user = &list[i]
		}
	}
	if user == nil {
		return out, fmt.Errorf("Jellyfin has no account %s any more", a.RemoteID)
	}
	if user.Policy.IsAdministrator {
		return out, fmt.Errorf("the Jellyfin account %q is an administrator: Core leaves its sessions alone", user.Name)
	}
	device := fmt.Sprintf("pocket-core-%s-%d", user.ID, t.Clock().UnixMilli())
	authz := jfAuth("", a.Email, device)
	token, qcErr := c.quickConnect(ctx, t, user.ID, strings.TrimSpace(a.Fields["API_KEY"]), authz)
	if token == "" {
		password := RandomPassword(40)
		if err := c.setPassword(ctx, t, user.ID, password); err != nil {
			return out, fmt.Errorf("Quick Connect did not work (%v) and setting a new password failed: %w", qcErr, err)
		}
		var login struct {
			AccessToken string `json:"AccessToken"`
		}
		if err := c.call(ctx, t, http.MethodPost, "/Users/AuthenticateByName", authz, map[string]string{"Username": user.Name, "Pw": password}, &login, password); err != nil || login.AccessToken == "" {
			if err == nil {
				err = errors.New("no access token in the answer")
			}
			return out, fmt.Errorf("signing in as the Jellyfin account after setting its new password failed: %w", err)
		}
		token = login.AccessToken
		out.Note = "Quick Connect is off in Jellyfin, so Core set a new random password to sign in: Jellyfin signed the account out on every device"
	}
	out.Fields = map[string]string{"API_KEY": token, "USER_ID": user.ID}
	var devs struct {
		Items []jfDevice `json:"Items"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/Devices?userId="+url.QueryEscape(user.ID), nil, &devs); err != nil {
		return out, fmt.Errorf("the new token is saved but listing the old sessions failed, so they may still work: %w", err)
	}
	var failed []string
	for _, d := range devs.Items {
		// only Core's own devices of this account: never the person's phone or TV
		if d.ID == device || d.AppName != JellyfinClient || !strings.HasPrefix(d.ID, "pocket-core-") || (d.LastUserID != "" && !jfSameID(d.LastUserID, user.ID)) {
			continue
		}
		if err := c.admin(ctx, t, http.MethodDelete, "/Devices?id="+url.QueryEscape(d.ID), nil, nil); err != nil && jfStatus(err) != http.StatusNotFound {
			failed = append(failed, d.ID+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("the new token is saved but deleting the old session(s) failed, so they still work: %s", strings.Join(failed, "; "))
	}
	if old := strings.TrimSpace(a.Fields["API_KEY"]); old != "" && c.call(ctx, t, http.MethodGet, "/Users/Me", fmt.Sprintf(`MediaBrowser Token="%s"`, strings.ReplaceAll(old, `"`, "")), nil, nil, old) == nil {
		return out, errors.New(OldStillWorks)
	}
	return out, nil
}

// quickConnect opens a new session for the account without its password.
func (c jellyfin) quickConnect(ctx context.Context, t Target, userID, old, authz string) (string, error) {
	var qc struct {
		Secret string `json:"Secret"`
		Code   string `json:"Code"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/QuickConnect/Initiate", authz, nil, &qc); err != nil || qc.Secret == "" || qc.Code == "" {
		if err == nil {
			err = errors.New("no code in the answer")
		}
		return "", fmt.Errorf("Quick Connect: %w", err)
	}
	authorized := false
	if old != "" {
		// As the person, with the token being replaced. Only the token: Jellyfin
		// rewrites a session's device fields from the header it is used with.
		hdr := fmt.Sprintf(`MediaBrowser Token="%s"`, strings.ReplaceAll(old, `"`, ""))
		err := c.call(ctx, t, http.MethodPost, "/QuickConnect/Authorize?code="+url.QueryEscape(qc.Code), hdr, nil, nil, old, qc.Secret)
		authorized = err == nil
	}
	if !authorized {
		// the admin for the account (Jellyfin 10.9+)
		q := url.Values{"code": {qc.Code}, "userId": {userID}}
		if err := c.admin(ctx, t, http.MethodPost, "/QuickConnect/Authorize?"+q.Encode(), nil, nil, qc.Secret); err != nil {
			return "", fmt.Errorf("Quick Connect: authorizing failed: %w", err)
		}
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        jfUser `json:"User"`
	}
	if err := c.call(ctx, t, http.MethodPost, "/Users/AuthenticateWithQuickConnect", authz, map[string]string{"Secret": qc.Secret}, &login, qc.Secret); err != nil || login.AccessToken == "" {
		if err == nil {
			err = errors.New("no access token in the answer")
		}
		return "", fmt.Errorf("Quick Connect: signing in failed: %w", err)
	}
	if login.User.ID != "" && !jfSameID(login.User.ID, userID) {
		return "", fmt.Errorf("Quick Connect signed in as another account (%s)", login.User.ID)
	}
	return login.AccessToken, nil
}

// setPassword sets an account's password as the admin (/Users/{id}/Password, else the 10.10+ /Users/Password?userId=).
func (c jellyfin) setPassword(ctx context.Context, t Target, userID, password string) error {
	body := map[string]any{"CurrentPw": "", "NewPw": password, "ResetPassword": false}
	err := c.admin(ctx, t, http.MethodPost, "/Users/"+url.PathEscape(userID)+"/Password", body, nil, password)
	if s := jfStatus(err); s == http.StatusNotFound || s == http.StatusMethodNotAllowed {
		err = c.admin(ctx, t, http.MethodPost, "/Users/Password?userId="+url.QueryEscape(userID), body, nil, password)
	}
	return err
}

// jfSameID compares Jellyfin ids written with or without dashes.
func jfSameID(a, b string) bool {
	n := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }
	return a != "" && n(a) == n(b)
}

// ---- Audiobookshelf ----

// absKeyPrefix starts the name of every key Core mints ("Pocket Core · <email>").
const absKeyPrefix = "Pocket Core"

// RotateKey makes a new API key for the account (as the admin) and deletes
// the account's older keys Core made.
func (c audiobookshelf) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	list, err := c.users(ctx, t)
	if err != nil {
		return out, err
	}
	var user *absUser
	for i := range list {
		if list[i].ID == a.RemoteID {
			user = &list[i]
		}
	}
	if user == nil {
		return out, fmt.Errorf("Audiobookshelf has no account %s any more", a.RemoteID)
	}
	if user.admin() {
		return out, fmt.Errorf("the Audiobookshelf account %q is an %s: Core leaves its keys alone", user.Username, user.Type)
	}
	var made struct {
		APIKey struct {
			ID     string `json:"id"`
			APIKey string `json:"apiKey"`
		} `json:"apiKey"`
	}
	email := strings.ToLower(strings.TrimSpace(a.Email))
	kin := map[string]any{"name": absKeyPrefix + " · " + email, "userId": user.ID, "isActive": true}
	if err := c.admin(ctx, t, http.MethodPost, "/api/api-keys", kin, &made); err != nil || made.APIKey.APIKey == "" {
		if err == nil {
			err = errors.New("no key in the answer")
		}
		return out, fmt.Errorf("making the new Audiobookshelf key failed: %w", err)
	}
	out.Fields = map[string]string{"TOKEN": made.APIKey.APIKey, "USER_ID": user.ID}
	var keys struct {
		APIKeys []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			UserID string `json:"userId"`
			User   *struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"apiKeys"`
	}
	if err := c.admin(ctx, t, http.MethodGet, "/api/api-keys", nil, &keys); err != nil {
		return out, fmt.Errorf("the new key is saved but listing the old ones failed, so they still work: %w", err)
	}
	var failed []string
	for _, k := range keys.APIKeys {
		owner := k.UserID
		if owner == "" && k.User != nil {
			owner = k.User.ID
		}
		if k.ID == made.APIKey.ID || owner != user.ID || !strings.HasPrefix(k.Name, absKeyPrefix) {
			continue
		}
		if err := c.admin(ctx, t, http.MethodDelete, "/api/api-keys/"+url.PathEscape(k.ID), nil, nil); err != nil && absStatus(err) != http.StatusNotFound {
			failed = append(failed, k.ID+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return out, fmt.Errorf("the new key is saved but deleting the old one(s) failed, so they still work: %s", strings.Join(failed, "; "))
	}
	if old := strings.TrimSpace(a.Fields["TOKEN"]); old != "" && c.call(ctx, t, http.MethodGet, "/api/me", old, nil, nil) == nil {
		return out, errors.New(OldStillWorks)
	}
	return out, nil
}

// ---- Paperless-ngx ----

// RotateKey regenerates the account's token (Paperless keeps one per
// account: the old one is deleted with it). It acts with the stored token;
// when that no longer works, the superuser sets a new random password, Core
// signs in once for a token and regenerates it.
func (c paperless) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	adm, err := plToken(t)
	if err != nil {
		return out, err
	}
	found, err := list[plUser](ctx, c, t, "/api/users/?username__iexact="+url.QueryEscape(a.RemoteID), adm)
	if err != nil {
		return out, err
	}
	var user *plUser
	for i := range found {
		if strings.EqualFold(found[i].Username, a.RemoteID) {
			user = &found[i]
		}
	}
	if user == nil || plSystemUser(user.Username) {
		return out, fmt.Errorf("Paperless has no account %q any more", a.RemoteID)
	}
	if user.IsSuperuser {
		return out, fmt.Errorf("the Paperless account %q is a superuser: Core leaves its token alone", user.Username)
	}
	tok := strings.TrimSpace(a.Fields["TOKEN"])
	if tok != "" {
		var ui struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		}
		err := c.call(ctx, t, http.MethodGet, "/api/ui_settings/", tok, nil, &ui)
		switch {
		case err == nil && !strings.EqualFold(ui.User.Username, user.Username):
			return out, fmt.Errorf("the stored token belongs to another Paperless account (%q): Core leaves it alone", ui.User.Username)
		case err != nil && plStatus(err) != http.StatusUnauthorized && plStatus(err) != http.StatusForbidden:
			return out, err
		case err != nil:
			tok = "" // gone: sign in instead
		}
	}
	if tok == "" {
		password := RandomPassword(40)
		if err := c.call(ctx, t, http.MethodPatch, "/api/users/"+strconv.FormatInt(user.ID, 10)+"/", adm, map[string]any{"password": password}, nil, password); err != nil {
			return out, fmt.Errorf("the stored token no longer works and setting a new password failed: %w", plNeedAdmin(err))
		}
		var got struct {
			Token string `json:"token"`
		}
		if err := c.call(ctx, t, http.MethodPost, "/api/token/", "", map[string]string{"username": user.Username, "password": password}, &got, password); err != nil || got.Token == "" {
			if err == nil {
				err = errors.New("no token in the answer")
			}
			return out, fmt.Errorf("signing in as the Paperless account after setting its new password failed: %w", err)
		}
		tok = got.Token
		out.Note = "the stored token no longer worked, so Core set a new random password on the account to sign in as it"
	}
	var fresh string
	if err := c.call(ctx, t, http.MethodPost, "/api/profile/generate_auth_token/", tok, nil, &fresh); err != nil || strings.TrimSpace(fresh) == "" {
		if err == nil {
			err = errors.New("no token in the answer")
		}
		return Rotated{}, fmt.Errorf("regenerating the Paperless token failed, the old one still works: %w", err)
	}
	out.Fields = map[string]string{"TOKEN": strings.TrimSpace(fresh)}
	if old := strings.TrimSpace(a.Fields["TOKEN"]); old != "" && old != out.Fields["TOKEN"] && c.call(ctx, t, http.MethodGet, "/api/ui_settings/", old, nil, nil) == nil {
		return out, errors.New(OldStillWorks)
	}
	return out, nil
}

// ---- Nextcloud ----

// RotateKey swaps the account's app password for a new one with Nextcloud's
// own rotation (the old one stops working at once). It needs the stored app
// password; Core never resets a Nextcloud password.
func (c nextcloud) RotateKey(ctx context.Context, t Target, a AccountRef) (Rotated, error) {
	var out Rotated
	uid, pass := strings.TrimSpace(a.Fields["USERNAME"]), strings.TrimSpace(a.Fields["APP_PASSWORD"])
	if uid == "" {
		uid = a.RemoteID
	}
	if pass == "" || !strings.EqualFold(uid, a.RemoteID) {
		return out, fmt.Errorf("the stored credential is not an app password of the Nextcloud account %s: Core leaves it alone", a.RemoteID)
	}
	who := ncAuth{user: uid, pass: pass}
	var me ncUser
	if err := c.ocs(ctx, t, http.MethodGet, "/ocs/v2.php/cloud/user", who, nil, &me); err != nil {
		if ncStatus(err) == http.StatusUnauthorized {
			return out, errors.New("the stored app password no longer works in Nextcloud (it was revoked already); set a new one by hand")
		}
		return out, err
	}
	if !strings.EqualFold(me.ID, a.RemoteID) {
		return out, fmt.Errorf("the stored app password belongs to another Nextcloud account (%s): Core leaves it alone", me.ID)
	}
	if me.admin() {
		return out, fmt.Errorf("the Nextcloud account %s is an admin: Core leaves its app passwords alone", me.ID)
	}
	var ap struct {
		AppPassword string `json:"apppassword"`
	}
	if err := c.ocs(ctx, t, http.MethodPost, "/ocs/v2.php/core/apppassword/rotate", who, nil, &ap); err != nil || ap.AppPassword == "" {
		if err == nil {
			err = errors.New("no app password in the answer")
		}
		return out, fmt.Errorf("rotating the Nextcloud app password failed, the old one still works: %w", err)
	}
	out.Fields = map[string]string{"USERNAME": me.ID, "APP_PASSWORD": ap.AppPassword}
	return out, nil
}
