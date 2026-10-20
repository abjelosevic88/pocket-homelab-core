package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
	"github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// keyEnv is a box with an admin, a service under a connector and Pocket apps
// reaching it through Core.
type keyEnv struct {
	*env
	t    *testing.T
	sv   int64
	base string
	apps map[string]*sdk.Client
}

func (k *keyEnv) call(method, path string, body, out any) int {
	k.t.Helper()
	code, txt := k.raw(method, path, "", body)
	if out != nil {
		_ = json.Unmarshal([]byte(txt), out)
	}
	return code
}

// newKeyEnv sets up the admin, the service with its connector on, and the apps.
func newKeyEnv(t *testing.T, admin, typ, url string, fields map[string]string, apps ...string) *keyEnv {
	t.Helper()
	k := &keyEnv{env: newEnv(t), t: t, apps: map[string]*sdk.Client{}}
	if c := k.call("POST", "/api/v1/auth/setup", map[string]string{"email": admin, "password": "password123", "name": "Owner"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var sv struct {
		ID int64 `json:"id"`
	}
	if c := k.call("POST", "/api/v1/services", map[string]any{"type": typ, "url": url, "public_url": "https://" + typ + ".example", "fields": fields}, &sv); c != 201 {
		t.Fatalf("create service %d", c)
	}
	k.sv, k.base = sv.ID, "/api/v1/services/"+itoa(sv.ID)+"/connector"
	if c := k.call("PUT", k.base, map[string]any{"enabled": true}, nil); c != 200 {
		t.Fatalf("connector on %d", c)
	}
	var settings store.Settings
	k.call("GET", "/api/v1/settings", nil, &settings)
	for _, slug := range apps {
		var reg struct {
			Token string `json:"token"`
		}
		pe := &env{t: t, srv: k.srv}
		if c := pe.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": slug, "name": "Pocket " + slug, "url": "http://" + slug,
			"services": []map[string]any{{"type": typ}}}, &reg); c != 201 && c != 200 {
			t.Fatalf("register %s: %d", slug, c)
		}
		k.apps[slug] = sdk.New(k.srv.URL, sdk.Options{Token: reg.Token, ServiceCache: -1})
	}
	return k
}

// person creates a member and provisions them.
func (k *keyEnv) person(email string) *store.User {
	k.t.Helper()
	var u store.User
	if c := k.call("POST", "/api/v1/users", map[string]string{"email": email, "name": strings.Split(email, "@")[0], "password": "password123", "role": "member"}, &u); c != 201 {
		k.t.Fatalf("create %s: %d", email, c)
	}
	return &u
}

func (k *keyEnv) provision(u *store.User) provisionResult {
	k.t.Helper()
	var pr struct {
		Results []provisionResult `json:"results"`
	}
	k.call("POST", k.base+"/provision", map[string]any{"user_id": u.ID}, &pr)
	if len(pr.Results) != 1 {
		k.t.Fatalf("provision %s: %+v", u.Email, pr)
	}
	return pr.Results[0]
}

// cred is the person's stored credential field.
func (k *keyEnv) cred(u *store.User, field string) (string, string) {
	ovs, _ := k.st.ListOverrides(context.Background(), u.ID)
	o := ovs[k.sv]
	return o.Fields[field], o.Source
}

func (k *keyEnv) account(u *store.User) store.ConnectorAccount {
	a, err := k.st.GetConnectorAccount(context.Background(), k.sv, u.ID)
	if err != nil {
		return store.ConnectorAccount{}
	}
	return *a
}

// linkID is the service link of an app.
func (k *keyEnv) linkID(app string) string {
	k.t.Helper()
	var links []map[string]any
	k.call("GET", "/api/v1/links", nil, &links)
	for _, l := range links {
		if l["app_slug"] == app && strings.HasPrefix(l["target_slug"].(string), "service:") {
			return itoa(int64(l["id"].(float64)))
		}
	}
	k.t.Fatalf("no service link for %s", app)
	return ""
}

// optOut switches the app's service link off (or on) for the person, as them.
func (k *keyEnv) optOut(u *store.User, app string, off bool) {
	k.t.Helper()
	pe := &env{t: k.t, srv: k.srv}
	if c := pe.do("POST", "/api/v1/auth/login", "", map[string]string{"email": u.Email, "password": "password123"}, nil); c != 200 {
		k.t.Fatalf("login %s: %d", u.Email, c)
	}
	if c := pe.do("PUT", "/api/v1/me/links/"+k.linkID(app), "", map[string]bool{"enabled": !off}, nil); c != 200 {
		k.t.Fatalf("my switch: %d", c)
	}
	k.s.rotations.Wait()
}

// boxSwitch flips the app's service link for everyone.
func (k *keyEnv) boxSwitch(app string, on bool) {
	k.t.Helper()
	if c := k.call("PUT", "/api/v1/links/"+k.linkID(app), map[string]any{"enabled": on}, nil); c != 200 {
		k.t.Fatalf("box switch: %d", c)
	}
	k.s.rotations.Wait()
}

func (k *keyEnv) serviceFor(app string, u *store.User, typ string) (string, error) {
	got, err := k.apps[app].ServiceFor(context.Background(), u.Email, typ)
	if err != nil {
		return "", err
	}
	return got.Key, nil
}

func (k *keyEnv) events(topic string) []map[string]any {
	var evs []store.Event
	k.call("GET", "/api/v1/events?topic="+topic, nil, &evs)
	out := []map[string]any{}
	for _, e := range evs {
		var p map[string]any
		_ = json.Unmarshal(e.Payload, &p)
		out = append(out, p)
	}
	return out
}

func TestKeyReplacementImmich(t *testing.T) {
	im := newFakeImmich(t)
	k := newKeyEnv(t, "admin@b.c", "immich", im.URL, map[string]string{"API_KEY": im.adminKey}, "day", "travel")
	ctx := context.Background()
	owner, _ := k.st.GetUserByEmail(ctx, "admin@b.c")
	if r := k.provision(owner); r.Status != "linked" {
		t.Fatalf("owner %+v", r)
	}
	// The owner's apps use the box key, claimed as his own (it is his account's).
	if _, err := ClaimBoxCredentials(ctx, k.st, owner, []string{"immich"}, false); err != nil {
		t.Fatal(err)
	}
	sani, kid, existing, own := k.person("sani@example.com"), k.person("kid@example.com"), k.person("existing@example.com"), k.person("own@example.com")
	k.call("PUT", "/api/v1/users/"+itoa(own.ID)+"/credentials/"+itoa(k.sv), map[string]any{"fields": map[string]string{"API_KEY": "my-own-key"}}, nil)
	for _, u := range []*store.User{sani, kid, existing, own} {
		k.provision(u)
	}
	if a := k.account(existing); a.Status != "linked" {
		t.Fatalf("existing %+v", a)
	}
	ownIU, _ := im.userByEmail("own@example.com")
	im.mu.Lock()
	im.keys["my-own-key"] = ownIU.ID
	im.mu.Unlock()
	saniIU, _ := im.userByEmail("sani@example.com")
	old, src := k.cred(sani, "API_KEY")
	if src != store.OverrideByConnector || im.keys[old] != saniIU.ID {
		t.Fatalf("sani's minted key %q %s", old, src)
	}
	if got, err := k.serviceFor("day", sani, "immich"); err != nil || got != old {
		t.Fatalf("day gets sani's key: %q %v", got, err)
	}

	// Sani switches Day off for herself: her key is replaced in Immich.
	k.optOut(sani, "day", true)
	fresh, src := k.cred(sani, "API_KEY")
	im.mu.Lock()
	_, oldAlive := im.keys[old]
	freshOwner := im.keys[fresh]
	im.mu.Unlock()
	if oldAlive || fresh == old || fresh == "" || freshOwner != saniIU.ID || src != store.OverrideByConnector {
		t.Fatalf("not replaced: old alive=%v fresh=%q owner=%q src=%s", oldAlive, fresh, freshOwner, src)
	}
	if a := k.account(sani); a.KeyReplacedAt == "" || a.KeyError != "" || a.Status != "ok" {
		t.Fatalf("sani's account %+v", a)
	}
	// Day is refused; Travel, still allowed, gets the new key; nobody else's changed.
	var le *sdk.LinkError
	if _, err := k.serviceFor("day", sani, "immich"); !errors.As(err, &le) || le.Reason != linkOffPerson {
		t.Fatalf("day after opt-out: %v", err)
	}
	if got, err := k.serviceFor("travel", sani, "immich"); err != nil || got != fresh {
		t.Fatalf("travel: %q %v", got, err)
	}
	if kidKey, _ := k.cred(kid, "API_KEY"); im.keys[kidKey] == "" {
		t.Fatalf("kid's key was touched by sani's switch")
	}
	evs := k.events(topicKeyReplaced)
	if len(evs) != 1 || evs[0]["user_email"] != "sani@example.com" || evs[0]["reason"] != linkOffPerson || evs[0]["app"] != "day" || evs[0]["type"] != "immich" {
		t.Fatalf("events %+v", evs)
	}
	for _, e := range evs {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), fresh) || strings.Contains(string(b), old) {
			t.Fatalf("a key in an event: %s", b)
		}
	}
	// Switching it back on changes nothing in Immich: Day now gets the new key.
	k.optOut(sani, "day", false)
	if got, err := k.serviceFor("day", sani, "immich"); err != nil || got != fresh {
		t.Fatalf("day back on: %q %v", got, err)
	}

	// A connector-written entry holding the box key is never replaced.
	boxy := k.person("boxy@example.com")
	k.provision(boxy)
	if err := k.st.SaveOverride(ctx, &store.Override{ServiceID: k.sv, UserID: boxy.ID, Fields: map[string]string{"API_KEY": im.adminKey}, Source: store.OverrideByConnector}, k.entryForTest(ctx)); err != nil {
		t.Fatal(err)
	}
	// Kid's stored key was deleted in Immich and the admin key may not set passwords: replacing fails, and says so.
	kidKey, _ := k.cred(kid, "API_KEY")
	im.mu.Lock()
	delete(im.keys, kidKey)
	im.noReset = true
	im.mu.Unlock()

	// The admin switches Day off for everyone.
	before, _ := k.cred(sani, "API_KEY")
	k.boxSwitch("day", false)
	after, _ := k.cred(sani, "API_KEY")
	if after == before || im.keys[after] != saniIU.ID {
		t.Fatalf("box-wide switch-off did not replace sani's key")
	}
	if a := k.account(kid); !strings.Contains(a.KeyError, "adminUser.update") || a.Status != "ok" {
		t.Fatalf("kid's failure not recorded: %+v", a)
	}
	if now, _ := k.cred(kid, "API_KEY"); now != kidKey {
		t.Fatalf("a failed replacement changed kid's stored key")
	}
	if a := k.account(boxy); !strings.Contains(a.KeyError, "box-wide") {
		t.Fatalf("boxy %+v", a)
	}
	if v, _ := k.cred(boxy, "API_KEY"); v != im.adminKey {
		t.Fatalf("boxy's entry changed")
	}
	// Untouched: the owner (linked, box key claimed as his), existing (linked), own (set by hand), the box key itself.
	if v, src := k.cred(owner, "API_KEY"); v != im.adminKey || src != store.OverrideByUser {
		t.Fatalf("owner's credential changed: %q %s", v, src)
	}
	if v, _ := k.cred(own, "API_KEY"); v != "my-own-key" || im.keys["my-own-key"] != ownIU.ID {
		t.Fatalf("own's key touched")
	}
	if ovs, _ := k.st.ListOverrides(ctx, existing.ID); len(ovs) != 0 {
		t.Fatalf("existing got a credential %+v", ovs)
	}
	sv, _ := k.st.GetService(ctx, k.sv)
	if sv.Fields["API_KEY"] != im.adminKey {
		t.Fatalf("box key changed")
	}
	var tr struct {
		OK bool `json:"ok"`
	}
	if k.call("POST", k.base+"/test", nil, &tr); !tr.OK {
		t.Fatalf("the box key stopped working")
	}
	if fails := k.events(topicKeyReplaceFails); len(fails) != 1 || fails[0]["user_email"] != "kid@example.com" {
		t.Fatalf("failure events %+v", fails)
	}

	// With password resets allowed, a stale key is replaced by signing in once.
	im.mu.Lock()
	im.noReset = false
	im.mu.Unlock()
	k.boxSwitch("day", true)
	k.boxSwitch("day", false)
	newKid, _ := k.cred(kid, "API_KEY")
	kidIU, _ := im.userByEmail("kid@example.com")
	if a := k.account(kid); newKid == kidKey || im.keys[newKid] != kidIU.ID || a.KeyError != "" || !strings.Contains(a.KeyNote, "password") || im.resets != 1 {
		t.Fatalf("kid via sign-in: key changed=%v owner=%q account=%+v resets=%d", newKid != kidKey, im.keys[newKid], a, im.resets)
	}
	_, _, sessions, _, _ := im.snapshot()
	if sessions != 0 {
		t.Fatalf("the sign-in session was left open")
	}

	// An old key that survives the delete is caught: the new one is saved, the account says so.
	im.mu.Lock()
	im.keepOnDel = true
	im.mu.Unlock()
	prev, _ := k.cred(sani, "API_KEY")
	k.optOut(sani, "travel", true)
	cur, _ := k.cred(sani, "API_KEY")
	if a := k.account(sani); cur == prev || a.KeyError != connectors.OldStillWorks || a.KeyReplacedAt == "" {
		t.Fatalf("surviving key: changed=%v account=%+v", cur != prev, a)
	}
	if evs := k.events(topicKeyReplaced); evs[0]["error"] != connectors.OldStillWorks {
		t.Fatalf("event %+v", evs[0])
	}
	im.mu.Lock()
	im.keepOnDel = false
	im.mu.Unlock()

	// The connector panel and the Links page show it.
	var rows []struct {
		Email         string `json:"email"`
		KeyReplacedAt string `json:"key_replaced_at"`
		KeyError      string `json:"key_error"`
	}
	k.call("GET", k.base+"/accounts", nil, &rows)
	shown := map[string]bool{}
	for _, r := range rows {
		shown[r.Email] = r.KeyReplacedAt != "" || r.KeyError != ""
	}
	if !shown["sani@example.com"] || !shown["kid@example.com"] || shown["existing@example.com"] || shown["admin@b.c"] {
		t.Fatalf("accounts %+v", rows)
	}
	var view struct {
		KeyRotation string `json:"key_rotation"`
	}
	k.call("GET", k.base, nil, &view)
	var links []map[string]any
	k.call("GET", "/api/v1/links", nil, &links)
	if view.KeyRotation != "replaces" || links[0]["key_rotation"] != "replaces" || len(links[0]["key_replacements"].([]any)) < 2 {
		t.Fatalf("views: %q %+v", view.KeyRotation, links[0])
	}
}

func (k *keyEnv) entryForTest(ctx context.Context) func(string) bool {
	sv, _ := k.st.GetService(ctx, k.sv)
	return k.s.entryFor(ctx, sv).IsSecret
}

// noRotate is a connector without the capability.
type noRotate struct{ connectors.Connector }

func TestKeyReplacementWithoutCapability(t *testing.T) {
	im := newFakeImmich(t)
	orig, _ := connectors.Get("immich")
	connectors.Register(noRotate{orig})
	t.Cleanup(func() { connectors.Register(orig) })
	k := newKeyEnv(t, "admin@b.c", "immich", im.URL, map[string]string{"API_KEY": im.adminKey}, "day")
	sani := k.person("sani@example.com")
	k.provision(sani)
	old, _ := k.cred(sani, "API_KEY")
	k.optOut(sani, "day", true)
	if now, _ := k.cred(sani, "API_KEY"); now != old || im.keys[old] == "" {
		t.Fatalf("a connector without the capability changed the key")
	}
	if a := k.account(sani); !strings.Contains(a.KeyError, "can't replace keys in") || a.KeyReplacedAt != "" {
		t.Fatalf("not said: %+v", a)
	}
	var view struct {
		KeyRotation string `json:"key_rotation"`
	}
	k.call("GET", k.base, nil, &view)
	if view.KeyRotation != "cannot" {
		t.Fatalf("panel says %q", view.KeyRotation)
	}
}

func TestKeyReplacementJellyfin(t *testing.T) {
	jf := newFakeJellyfin(t)
	k := newKeyEnv(t, "aleks@b.c", "jellyfin", jf.URL, map[string]string{"API_KEY": jf.adminKey}, "day")
	ctx := context.Background()
	owner, _ := k.st.GetUserByEmail(ctx, "aleks@b.c")
	if r := k.provision(owner); r.Status != "linked" {
		t.Fatalf("owner %+v", r)
	}
	kid := k.person("kid@b.c")
	if r := k.provision(kid); r.Status != "ok" || r.Credential != "connector" {
		t.Fatalf("kid %+v", r)
	}
	kidID := k.account(kid).RemoteID
	old, _ := k.cred(kid, "API_KEY")
	jf.mu.Lock()
	jf.sessions["phone-tok"] = kidID // kid's own phone app
	jf.devices["phone-tok"] = [2]string{"iphone-1", "Jellyfin Mobile"}
	jf.mu.Unlock()

	// Opt-out: a new session through Quick Connect, Core's old one deleted, the phone left alone.
	k.optOut(kid, "day", true)
	fresh, _ := k.cred(kid, "API_KEY")
	uid, _ := k.cred(kid, "USER_ID")
	jf.mu.Lock()
	oldAlive, phone, freshUser, dev := jf.sessions[old] != "", jf.sessions["phone-tok"] != "", jf.sessions[fresh], jf.devices[fresh]
	jf.mu.Unlock()
	if oldAlive || !phone || fresh == old || freshUser != kidID || uid != kidID || !strings.HasPrefix(dev[0], "pocket-core-"+kidID+"-") || dev[1] != "Pocket Core" {
		t.Fatalf("quick connect: old alive=%v phone=%v fresh user=%q uid=%q device=%v", oldAlive, phone, freshUser, uid, dev)
	}
	if a := k.account(kid); a.KeyReplacedAt == "" || a.KeyError != "" || a.KeyNote != "" || jf.pwChanges != 0 {
		t.Fatalf("account %+v, password changes %d", a, jf.pwChanges)
	}
	// The owner is linked: his entry (user id only) is not touched.
	if v, _ := k.cred(owner, "USER_ID"); v != "u-aleks" {
		t.Fatalf("owner %q", v)
	}

	// Quick Connect off: a new random password, and Jellyfin signs the account out everywhere.
	jf.mu.Lock()
	jf.qcOff = true
	jf.mu.Unlock()
	k.boxSwitch("day", false)
	again, _ := k.cred(kid, "API_KEY")
	jf.mu.Lock()
	freshAlive, againUser := jf.sessions[fresh] != "", jf.sessions[again]
	jf.mu.Unlock()
	if a := k.account(kid); again == fresh || freshAlive || againUser != kidID || jf.pwChanges != 1 || !strings.Contains(a.KeyNote, "Quick Connect is off") {
		t.Fatalf("password path: alive=%v user=%q changes=%d account=%+v", freshAlive, againUser, jf.pwChanges, a)
	}
}

func TestKeyReplacementAudiobookshelf(t *testing.T) {
	abs := newFakeABS(t)
	k := newKeyEnv(t, "aleks@b.c", "audiobookshelf", abs.URL, map[string]string{"TOKEN": abs.adminKey}, "day")
	ctx := context.Background()
	owner, _ := k.st.GetUserByEmail(ctx, "aleks@b.c")
	if r := k.provision(owner); r.Status != "linked" {
		t.Fatalf("owner %+v", r)
	}
	kid := k.person("kid@b.c")
	if r := k.provision(kid); r.Status != "ok" {
		t.Fatalf("kid %+v", r)
	}
	kidID := k.account(kid).RemoteID
	old, _ := k.cred(kid, "TOKEN")
	abs.mu.Lock()
	abs.keys["kid-own-key"] = kidID // a key kid made himself
	abs.keyMeta["k-own"] = [3]string{"kid-own-key", "my phone", kidID}
	abs.keys["root-key"] = "u-root"
	abs.keyMeta["k-root"] = [3]string{"root-key", "Pocket Core · aleks@b.c", "u-root"}
	abs.mu.Unlock()

	k.optOut(kid, "day", true)
	fresh, _ := k.cred(kid, "TOKEN")
	abs.mu.Lock()
	oldAlive, freshUser, own, root := abs.keys[old] != "", abs.keys[fresh], abs.keys["kid-own-key"] != "", abs.keys["root-key"] != ""
	abs.mu.Unlock()
	if oldAlive || fresh == old || freshUser != kidID || !own || !root {
		t.Fatalf("abs: old alive=%v fresh user=%q kid's own=%v root's=%v", oldAlive, freshUser, own, root)
	}
	if a := k.account(kid); a.KeyReplacedAt == "" || a.KeyError != "" {
		t.Fatalf("account %+v", a)
	}
	if got, err := k.serviceFor("day", kid, "audiobookshelf"); err == nil {
		t.Fatalf("day still gets a key: %q", got)
	}
	k.optOut(kid, "day", false)
	if got, err := k.serviceFor("day", kid, "audiobookshelf"); err != nil || got != fresh {
		t.Fatalf("back on: %q %v", got, err)
	}
}

func TestKeyReplacementPaperless(t *testing.T) {
	pl := newFakePaperless(t)
	k := newKeyEnv(t, "root@localhost", "paperless", pl.URL, map[string]string{"TOKEN": pl.adminTok}, "day")
	sam := k.person("sam@b.c")
	if r := k.provision(sam); r.Status != "ok" || r.Credential != "connector" {
		t.Fatalf("sam %+v", r)
	}
	old, _ := k.cred(sam, "TOKEN")
	k.boxSwitch("day", false)
	fresh, _ := k.cred(sam, "TOKEN")
	pl.mu.Lock()
	oldAlive, freshUser, adminAlive := pl.tokens[old] != "", pl.tokens[fresh], pl.tokens[pl.adminTok] != ""
	pl.mu.Unlock()
	if oldAlive || fresh == old || freshUser != "sam" || !adminAlive {
		t.Fatalf("paperless: old alive=%v fresh user=%q admin alive=%v", oldAlive, freshUser, adminAlive)
	}
	if a := k.account(sam); a.KeyReplacedAt == "" || a.KeyError != "" {
		t.Fatalf("account %+v", a)
	}
}

func TestKeyReplacementNextcloud(t *testing.T) {
	nc := newFakeNextcloud(t)
	k := newKeyEnv(t, "aleks@b.c", "nextcloud", nc.URL, map[string]string{"USERNAME": "aleks", "APP_PASSWORD": "aleks-app-pw", "ADMIN_PASSWORD": "aleks-login-pw"}, "day")
	sam := k.person("sam@b.c")
	if r := k.provision(sam); r.Status != "ok" || r.Credential != "connector" {
		t.Fatalf("sam %+v", r)
	}
	old, _ := k.cred(sam, "APP_PASSWORD")
	k.optOut(sam, "day", true)
	fresh, _ := k.cred(sam, "APP_PASSWORD")
	user, _ := k.cred(sam, "USERNAME")
	nc.mu.Lock()
	oldAlive, freshUser, adminAlive := nc.appPass[old] != "", nc.appPass[fresh], nc.appPass["aleks-app-pw"] == "aleks"
	nc.mu.Unlock()
	if oldAlive || fresh == old || freshUser != "sam" || user != "sam" || !adminAlive {
		t.Fatalf("nextcloud: old alive=%v fresh user=%q username=%q admin alive=%v", oldAlive, freshUser, user, adminAlive)
	}
}
