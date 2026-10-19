package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	pccrypto "github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// fakeImmich is a small stand-in for an Immich v3 server.
type fakeImmich struct {
	*httptest.Server
	mu        sync.Mutex
	adminKey  string
	memberKey string
	users     []fakeImmichUser
	passwords map[string]string // email → password sent at creation
	keys      map[string]string // minted secret → user id
	keyNames  []string
	sessions  map[string]string // access token → user id
	config    map[string]any
	workflows map[string]fakeWorkflow
	wfCreates int
	creates   int
	configPut int
	n         int
}

type fakeWorkflow struct {
	ID          string `json:"id"`
	Owner       string `json:"-"`
	Trigger     string `json:"trigger"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Logging     bool   `json:"logging"`
	Steps       []struct {
		Method  string         `json:"method"`
		Enabled bool           `json:"enabled"`
		Config  map[string]any `json:"config"`
	} `json:"steps"`
}

type fakeImmichUser struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"isAdmin"`
}

func defaultImmichConfig() map[string]any {
	return map[string]any{
		"ffmpeg":          map[string]any{"crf": 23, "preset": "ultrafast", "targetVideoCodec": "h264"},
		"machineLearning": map[string]any{"enabled": true, "clip": map[string]any{"enabled": true, "modelName": "ViT-B-32__openai"}},
		"passwordLogin":   map[string]any{"enabled": true},
		"server":          map[string]any{"externalDomain": "https://photos.example", "loginPageMessage": ""},
		"notifications":   map[string]any{"smtp": map[string]any{"enabled": false, "transport": map[string]any{"password": "smtp-secret"}}},
		"oauth": map[string]any{"enabled": false, "issuerUrl": "", "clientId": "", "clientSecret": "", "scope": "openid email profile",
			"buttonText": "Login with OAuth", "autoRegister": true, "autoLaunch": false, "mobileOverrideEnabled": false, "mobileRedirectUri": "",
			"signingAlgorithm": "RS256", "profileSigningAlgorithm": "none", "tokenEndpointAuthMethod": "client_secret_post", "timeout": 30000,
			"storageLabelClaim": "preferred_username", "storageQuotaClaim": "immich_quota", "roleClaim": "immich_role", "defaultStorageQuota": nil,
			"allowInsecureRequests": false, "prompt": "", "endSessionEndpoint": ""},
	}
}

func newFakeImmich(t *testing.T) *fakeImmich {
	f := &fakeImmich{adminKey: "immich-admin-key", memberKey: "immich-member-key", passwords: map[string]string{}, keys: map[string]string{},
		sessions: map[string]string{}, config: defaultImmichConfig(), workflows: map[string]fakeWorkflow{},
		users: []fakeImmichUser{
			{ID: "u-admin", Email: "admin@b.c", Name: "Owner", IsAdmin: true},
			{ID: "u-existing", Email: "existing@example.com", Name: "Existing"},
		}}
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	deny := func(w http.ResponseWriter, code int) {
		msg := "Authentication required"
		if code == 403 {
			msg = "Forbidden resource"
		}
		reply(w, code, map[string]any{"message": msg, "statusCode": code})
	}
	// adminOnly answers for a request that needs the admin key: true = go on.
	adminOnly := func(w http.ResponseWriter, r *http.Request) bool {
		switch r.Header.Get("x-api-key") {
		case f.adminKey:
			return true
		case f.memberKey:
			deny(w, 403)
		default:
			deny(w, 401)
		}
		return false
	}
	session := func(r *http.Request) string {
		return f.sessions[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/server/version", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"major": 3, "minor": 3, "patch": 3, "prerelease": nil})
	})
	mux.HandleFunc("GET /api/api-keys/me", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("x-api-key") {
		case f.adminKey:
			reply(w, 200, map[string]any{"id": "k1", "name": "core", "permissions": []string{"all"}})
		case f.memberKey:
			reply(w, 200, map[string]any{"id": "k2", "name": "mine", "permissions": []string{"asset.read"}})
		default:
			deny(w, 401)
		}
	})
	mux.HandleFunc("GET /api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if adminOnly(w, r) {
			reply(w, 200, f.users)
		}
	})
	mux.HandleFunc("POST /api/admin/users", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !adminOnly(w, r) {
			return
		}
		var in struct {
			Email, Name, Password string
			ShouldChangePassword  bool
			IsAdmin               bool
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, u := range f.users {
			if strings.EqualFold(u.Email, in.Email) {
				reply(w, 400, map[string]any{"message": "User exists"})
				return
			}
		}
		if in.Password == "" || in.Name == "" || in.ShouldChangePassword || in.IsAdmin {
			reply(w, 400, map[string]any{"message": "bad create body"})
			return
		}
		f.n++
		f.creates++
		u := fakeImmichUser{ID: fmt.Sprintf("u-%d", f.n), Email: in.Email, Name: in.Name}
		f.users = append(f.users, u)
		f.passwords[in.Email] = in.Password
		reply(w, 201, u)
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var in struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		for _, u := range f.users {
			if u.Email == in.Email && f.passwords[in.Email] != "" && f.passwords[in.Email] == in.Password {
				tok := "sess-" + pccrypto.Token("", 8)
				f.sessions[tok] = u.ID
				reply(w, 201, map[string]any{"accessToken": tok, "userId": u.ID, "userEmail": u.Email})
				return
			}
		}
		reply(w, 401, map[string]any{"message": "Incorrect email or password"})
	})
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := session(r)
		if uid == "" {
			deny(w, 401)
			return
		}
		var in struct {
			Name        string
			Permissions []string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		secret := "imk_" + pccrypto.Token("", 16)
		f.keys[secret] = uid
		f.keyNames = append(f.keyNames, in.Name)
		reply(w, 201, map[string]any{"id": "key-1", "name": in.Name, "secret": secret, "permissions": in.Permissions})
	})
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.sessions, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		reply(w, 200, map[string]any{"successful": true})
	})
	mux.HandleFunc("GET /api/system-config", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if adminOnly(w, r) {
			reply(w, 200, f.config)
		}
	})
	mux.HandleFunc("PUT /api/system-config", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !adminOnly(w, r) {
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.config = in
		f.configPut++
		reply(w, 200, in)
	})
	// keyUser is whose API key a request carries.
	keyUser := func(r *http.Request) string {
		k := r.Header.Get("x-api-key")
		if k == f.adminKey {
			return "u-admin"
		}
		return f.keys[k]
	}
	mux.HandleFunc("GET /api/users/me", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := keyUser(r)
		for _, u := range f.users {
			if uid != "" && u.ID == uid {
				reply(w, 200, u)
				return
			}
		}
		deny(w, 401)
	})
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := keyUser(r)
		if uid == "" {
			deny(w, 401)
			return
		}
		out := []fakeWorkflow{} // a workflow belongs to whoever created it
		for _, wf := range f.workflows {
			if wf.Owner == uid {
				out = append(out, wf)
			}
		}
		reply(w, 200, out)
	})
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		uid := keyUser(r)
		if uid == "" {
			deny(w, 401)
			return
		}
		var in fakeWorkflow
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Trigger != "AssetCreate" && in.Trigger != "AssetMetadataExtraction" {
			reply(w, 400, map[string]any{"message": "bad trigger"})
			return
		}
		if len(in.Steps) != 1 || in.Steps[0].Method != "immich-plugin-core#webhook" || in.Steps[0].Config["url"] == nil {
			reply(w, 400, map[string]any{"message": "Unknown method or missing url"})
			return
		}
		f.n++
		f.wfCreates++
		in.ID = fmt.Sprintf("wf-%d", f.n)
		in.Owner = uid
		f.workflows[in.ID] = in
		reply(w, 201, in)
	})
	mux.HandleFunc("DELETE /api/workflows/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		wf, ok := f.workflows[r.PathValue("id")]
		if !ok || wf.Owner != keyUser(r) {
			reply(w, 400, map[string]any{"message": "Not found or no workflow.delete access"})
			return
		}
		delete(f.workflows, wf.ID)
		w.WriteHeader(204)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeImmich) userByEmail(email string) (fakeImmichUser, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			return u, true
		}
	}
	return fakeImmichUser{}, false
}

// workflowsOf lists a user's workflows by trigger.
func (f *fakeImmich) workflowsOf(uid string) map[string]fakeWorkflow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]fakeWorkflow{}
	for _, wf := range f.workflows {
		if wf.Owner == uid {
			out[wf.Trigger] = wf
		}
	}
	return out
}

func (f *fakeImmich) workflowCount() (n, creates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.workflows), f.wfCreates
}

func (f *fakeImmich) snapshot() (creates, keys, sessions, puts int, cfg map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(f.config)
	_ = json.Unmarshal(b, &cfg)
	return f.creates, len(f.keys), len(f.sessions), f.configPut, cfg
}

// syncBuffer is a log sink safe for the server's background goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// raw does a request and returns the status and the body as text.
func (e *env) raw(method, path, auth string, body any) (int, string) {
	e.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	for _, c := range e.cj {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	if cs := res.Cookies(); len(cs) > 0 {
		e.cj = cs
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	return res.StatusCode, buf.String()
}

type accountRowT struct {
	UserID     int64  `json:"user_id"`
	Email      string `json:"email"`
	Status     string `json:"status"`
	RemoteID   string `json:"remote_id"`
	Error      string `json:"error"`
	Credential string `json:"credential"`
	InAudience bool   `json:"in_audience"`
}

func TestImmichConnector(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnvLog(t, logs)
	im := newFakeImmich(t)
	ctx := context.Background()
	responses := []string{} // every API answer, to prove the password never left Core's process
	call := func(method, path string, body any, out any) int {
		t.Helper()
		code, txt := e.raw(method, path, "", body)
		responses = append(responses, txt)
		if out != nil {
			_ = json.Unmarshal([]byte(txt), out)
		}
		return code
	}

	if c := call("POST", "/api/v1/auth/setup", map[string]string{"email": "admin@b.c", "password": "password123", "name": "Owner"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var e2 = *e // keep the admin cookie on e
	e2.cj = nil
	var sv struct {
		ID int64 `json:"id"`
	}
	if c := call("POST", "/api/v1/services", map[string]any{"type": "immich", "url": im.URL, "public_url": "https://photos.example", "fields": map[string]string{"API_KEY": im.adminKey}}, &sv); c != 201 {
		t.Fatalf("create service %d", c)
	}
	base := "/api/v1/services/" + itoa(sv.ID) + "/connector"

	// The catalog of connectors lists Immich and its service.
	var list []struct {
		Type      string `json:"type"`
		ServiceID int64  `json:"service_id"`
		Connector struct {
			Enabled bool `json:"enabled"`
		} `json:"connector"`
	}
	c := call("GET", "/api/v1/connectors", nil, &list)
	found := false
	for _, x := range list {
		found = found || (x.Type == "immich" && x.ServiceID == sv.ID && !x.Connector.Enabled)
	}
	if c != 200 || !found {
		t.Fatalf("connectors %d %+v", c, list)
	}

	// Test: ok with the admin key; a member's key gets an error naming the permissions.
	var tr struct {
		OK   bool   `json:"ok"`
		Err  string `json:"error"`
		Info struct {
			Version string `json:"version"`
			Admin   bool   `json:"admin"`
			Users   int    `json:"users"`
		} `json:"info"`
	}
	call("POST", base+"/test", nil, &tr)
	if !tr.OK || tr.Info.Version != "3.3.3" || !tr.Info.Admin || tr.Info.Users != 2 {
		t.Fatalf("test %+v", tr)
	}
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"fields": map[string]string{"API_KEY": im.memberKey}}, nil)
	tr.OK, tr.Err = false, ""
	call("POST", base+"/test", nil, &tr)
	if tr.OK || !strings.Contains(tr.Err, "ADMIN") || !strings.Contains(tr.Err, "adminUser.read") || !strings.Contains(tr.Err, "systemConfig.update") {
		t.Fatalf("member key should fail clearly: %+v", tr)
	}
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"fields": map[string]string{"API_KEY": im.adminKey}}, nil)

	// Nothing happens while the connector is off.
	if c := call("POST", base+"/provision", map[string]any{"all": true}, nil); c != 400 {
		t.Fatalf("provision while off %d", c)
	}
	if c := call("PUT", base, map[string]any{"enabled": true, "provision_on_create": true}, nil); c != 200 {
		t.Fatalf("enable %d", c)
	}

	// A new person gets an Immich account and their own key, in the background.
	var sani store.User
	if c := call("POST", "/api/v1/users", map[string]string{"email": "sani@example.com", "name": "Sani", "password": "password123", "role": "member"}, &sani); c != 201 {
		t.Fatalf("create sani %d", c)
	}
	accounts := func() map[string]accountRowT {
		var rows []accountRowT
		call("GET", base+"/accounts", nil, &rows)
		out := map[string]accountRowT{}
		for _, r := range rows {
			out[r.Email] = r
		}
		return out
	}
	waitFor(t, "sani provisioned", func() bool { return accounts()["sani@example.com"].Status == "ok" })
	iu, ok := im.userByEmail("sani@example.com")
	if !ok || iu.Name != "Sani" || iu.IsAdmin {
		t.Fatalf("immich user %+v", iu)
	}
	creates, keys, sessions, _, _ := im.snapshot()
	if creates != 1 || keys != 1 || sessions != 0 || im.keyNames[0] != "Pocket Core" {
		t.Fatalf("creates=%d keys=%d open sessions=%d names=%v", creates, keys, sessions, im.keyNames)
	}
	row := accounts()["sani@example.com"]
	if row.RemoteID != iu.ID || row.Credential != "connector" {
		t.Fatalf("account row %+v", row)
	}
	ovs, _ := e.st.ListOverrides(ctx, sani.ID)
	minted := ovs[sv.ID].Fields["API_KEY"]
	if ovs[sv.ID].Source != store.OverrideByConnector || im.keys[minted] != iu.ID {
		t.Fatalf("override %+v", ovs[sv.ID])
	}
	// Sani's phone receives Sani's key, not the admin's.
	var pair struct {
		Code string `json:"code"`
	}
	call("POST", "/api/v1/pair", map[string]int64{"user_id": sani.ID}, &pair)
	var prof Profile
	e2.do("POST", "/api/v1/pair/claim", "", map[string]string{"code": pair.Code, "name": "sani phone"}, &prof)
	if prof.Values["IMMICH_API_KEY"] != minted {
		t.Fatalf("profile key %q want %q", prof.Values["IMMICH_API_KEY"], minted)
	}

	// Idempotent: asking again changes nothing in Immich.
	var pr struct {
		Results []provisionResult `json:"results"`
	}
	call("POST", base+"/provision", map[string]any{"user_id": sani.ID}, &pr)
	if len(pr.Results) != 1 || pr.Results[0].Status != "ok" || pr.Results[0].Note != "already provisioned" {
		t.Fatalf("again %+v", pr)
	}
	// An existing Immich account with the same email is linked, without a key.
	var existing store.User
	call("POST", "/api/v1/users", map[string]string{"email": "existing@example.com", "name": "Existing"}, &existing)
	waitFor(t, "existing linked", func() bool { return accounts()["existing@example.com"].Status == "linked" })
	// Everyone missing: the admin's own Immich account is linked and left alone; sani is not redone.
	pr.Results = nil
	call("POST", base+"/provision", map[string]any{"all": true}, &pr)
	if len(pr.Results) != 1 || pr.Results[0].Email != "admin@b.c" || pr.Results[0].Status != "linked" || pr.Results[0].RemoteID != "u-admin" {
		t.Fatalf("all %+v", pr.Results)
	}
	creates, keys, _, _, _ = im.snapshot()
	if creates != 1 || keys != 1 {
		t.Fatalf("linking must not create or mint: creates=%d keys=%d", creates, keys)
	}
	if ovs, _ := e.st.ListOverrides(ctx, existing.ID); len(ovs) != 0 {
		t.Fatalf("linked account got a credential %+v", ovs)
	}

	// Outside the audience: nothing at all.
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"audience": "admins"}, nil)
	var kid store.User
	call("POST", "/api/v1/users", map[string]string{"email": "kid@example.com", "name": "Kid", "password": "password123"}, &kid)
	pr.Results = nil
	call("POST", base+"/provision", map[string]any{"user_id": kid.ID}, &pr)
	if len(pr.Results) != 1 || pr.Results[0].Status != "skipped" {
		t.Fatalf("outside audience %+v", pr.Results)
	}
	time.Sleep(200 * time.Millisecond) // let the background run for kid finish too
	if _, found := im.userByEmail("kid@example.com"); found {
		t.Fatalf("a person outside the audience got an Immich account")
	}
	if _, listed := accounts()["kid@example.com"]; listed {
		t.Fatalf("kid listed for an admins-only service")
	}
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"audience": "all"}, nil)

	// A person's own credential is never replaced.
	call("PUT", base, map[string]any{"provision_on_create": false}, nil)
	var own store.User
	call("POST", "/api/v1/users", map[string]string{"email": "own@example.com", "name": "Own"}, &own)
	call("PUT", "/api/v1/users/"+itoa(own.ID)+"/credentials/"+itoa(sv.ID), map[string]any{"fields": map[string]string{"API_KEY": "my-own-key"}}, nil)
	pr.Results = nil
	call("POST", base+"/provision", map[string]any{"user_id": own.ID}, &pr)
	if len(pr.Results) != 1 || pr.Results[0].Status != "ok" || pr.Results[0].Credential != "own" {
		t.Fatalf("own %+v", pr.Results)
	}
	if ovs, _ := e.st.ListOverrides(ctx, own.ID); ovs[sv.ID].Fields["API_KEY"] != "my-own-key" || ovs[sv.ID].Source != store.OverrideByUser {
		t.Fatalf("own override replaced: %+v", ovs[sv.ID])
	}
	if _, keys, _, _, _ = im.snapshot(); keys != 1 {
		t.Fatalf("a key was minted for someone with their own: %d", keys)
	}

	// The one-time password never appears in logs, API answers or the database.
	im.mu.Lock()
	pw := im.passwords["sani@example.com"]
	im.mu.Unlock()
	if len(pw) < 32 {
		t.Fatalf("weak password %q", pw)
	}
	if strings.Contains(logs.String(), pw) {
		t.Fatalf("password in logs")
	}
	for _, r := range responses {
		if strings.Contains(r, pw) {
			t.Fatalf("password in an API answer: %s", r)
		}
	}
	files, _ := filepath.Glob(filepath.Join(e.dir, "pocket-core.db*"))
	if len(files) == 0 {
		t.Fatal("no database files")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(pw)) {
			t.Fatalf("password stored in %s", f)
		}
		if bytes.Contains(b, []byte(minted)) {
			t.Fatalf("minted key stored unencrypted in %s", f)
		}
	}

	// Sign in with Pocket Core: only the oauth block changes, after a backup.
	_, _, _, _, before := im.snapshot()
	var view struct {
		SSOEnabled bool `json:"sso_enabled"`
		OIDCClient *struct {
			ClientID     string   `json:"client_id"`
			RedirectURIs []string `json:"redirect_uris"`
			ServiceID    int64    `json:"service_id"`
		} `json:"oidc_client"`
	}
	if c := call("PUT", base, map[string]any{"sso_enabled": true}, &view); c != 200 || !view.SSOEnabled || view.OIDCClient == nil || view.OIDCClient.ServiceID != sv.ID {
		t.Fatalf("sso on %d %+v", c, view)
	}
	_, _, _, puts, after := im.snapshot()
	if puts != 1 {
		t.Fatalf("config writes %d", puts)
	}
	for k := range before {
		if k != "oauth" && !reflect.DeepEqual(before[k], after[k]) {
			t.Fatalf("block %s changed: %v → %v", k, before[k], after[k])
		}
	}
	oa, ob := after["oauth"].(map[string]any), before["oauth"].(map[string]any)
	want := map[string]any{"enabled": true, "issuerUrl": "http://core.test", "clientId": view.OIDCClient.ClientID, "scope": "openid email profile",
		"buttonText": "Sign in with Pocket Core", "autoRegister": true, "autoLaunch": false, "mobileOverrideEnabled": true,
		"mobileRedirectUri": "https://photos.example/api/oauth/mobile-redirect"}
	for k, v := range want {
		if oa[k] != v {
			t.Fatalf("oauth.%s = %v, want %v", k, oa[k], v)
		}
	}
	for _, k := range []string{"signingAlgorithm", "tokenEndpointAuthMethod", "timeout", "storageLabelClaim", "roleClaim"} {
		if !reflect.DeepEqual(oa[k], ob[k]) {
			t.Fatalf("oauth.%s changed", k)
		}
	}
	if pl := after["passwordLogin"].(map[string]any); pl["enabled"] != true {
		t.Fatalf("password login turned off")
	}
	cl, err := e.st.GetOIDCClient(ctx, view.OIDCClient.ClientID)
	if err != nil || cl.SecretHash != pccrypto.Hash(oa["clientSecret"].(string)) {
		t.Fatalf("client secret given to Immich does not match Core's client")
	}
	wantRedirects := []string{"https://photos.example/auth/login", "https://photos.example/user-settings", "app.immich:///oauth-callback", "https://photos.example/api/oauth/mobile-redirect"}
	if !reflect.DeepEqual(cl.RedirectURIs, wantRedirects) {
		t.Fatalf("redirects %v", cl.RedirectURIs)
	}
	backups, _ := filepath.Glob(filepath.Join(e.dir, "connectors", "immich-system-config-*.json"))
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	var saved map[string]any
	bb, _ := os.ReadFile(backups[0])
	if json.Unmarshal(bb, &saved) != nil || !reflect.DeepEqual(saved, before) {
		t.Fatalf("backup does not hold the config as it was")
	}
	if fi, _ := os.Stat(backups[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", fi.Mode().Perm())
	}

	// The connector's client honours the service audience at /oidc/authorize.
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"audience": "users", "audience_users": []int64{sani.ID}}, nil)
	authorize := func(email string) *url.URL {
		t.Helper()
		pe := &env{t: t, srv: e.srv}
		if c := pe.do("POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": "password123"}, nil); c != 200 {
			t.Fatalf("login %s: %d", email, c)
		}
		q := url.Values{"client_id": {view.OIDCClient.ClientID}, "redirect_uri": {"https://photos.example/auth/login"}, "response_type": {"code"}, "scope": {"openid email profile"}, "state": {"st"}}
		req, _ := http.NewRequest("GET", e.srv.URL+"/oidc/authorize?"+q.Encode(), nil)
		for _, c := range pe.cj {
			req.AddCookie(c)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := res.Location()
		if loc == nil {
			t.Fatalf("authorize %s: no redirect (%d)", email, res.StatusCode)
		}
		return loc
	}
	if loc := authorize("kid@example.com"); loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Fatalf("outside the audience got %v", loc)
	}
	if loc := authorize("sani@example.com"); loc.Query().Get("code") == "" || loc.Host != "photos.example" {
		t.Fatalf("inside the audience got %v", loc)
	}
	call("PUT", "/api/v1/services/"+itoa(sv.ID), map[string]any{"audience": "all"}, nil)

	// Turning it off flips only oauth.enabled.
	if c := call("PUT", base, map[string]any{"sso_enabled": false}, &view); c != 200 || view.SSOEnabled {
		t.Fatalf("sso off %d", c)
	}
	_, _, _, _, off := im.snapshot()
	if oo := off["oauth"].(map[string]any); oo["enabled"] != false || oo["clientId"] != view.OIDCClient.ClientID {
		t.Fatalf("after disable %v", oo)
	}
	if backups, _ := filepath.Glob(filepath.Join(e.dir, "connectors", "immich-system-config-*.json")); len(backups) != 2 {
		t.Fatalf("disable should back up first too: %v", backups)
	}

	// Own's hand-set key is a real Immich key of their account.
	ownIU, _ := im.userByEmail("own@example.com")
	im.mu.Lock()
	im.keys["my-own-key"] = ownIU.ID
	im.mu.Unlock()
	adminIU, _ := im.userByEmail("admin@b.c")
	testImmichEvents(t, e, im, base, sv.ID, call, map[string]string{"admin@b.c": adminIU.ID, "sani@example.com": iu.ID, "own@example.com": ownIU.ID})

	// The person's page shows their status per connected service.
	var mine []struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	call("GET", "/api/v1/users/"+itoa(sani.ID)+"/connectors", nil, &mine)
	if len(mine) != 1 || mine[0].Type != "immich" || mine[0].Status != "ok" {
		t.Fatalf("user connectors %+v", mine)
	}

	// Admins only: a member's token and an app's token are refused like other admin service endpoints.
	_, memberTok, _ := e.st.CreateAPIToken(ctx, kid.ID, "kid")
	var settings store.Settings
	call("GET", "/api/v1/settings", nil, &settings)
	var reg struct {
		Token string `json:"token"`
	}
	e2.do("POST", "/api/v1/apps/register", settings.RegistrationKey, map[string]any{"slug": "kitchen", "name": "Pocket Kitchen", "url": "http://kitchen"}, &reg)
	for _, tok := range []string{memberTok, reg.Token} {
		for _, r := range []struct{ m, p string }{{"GET", "/api/v1/connectors"}, {"GET", base}, {"PUT", base}, {"POST", base + "/test"}, {"POST", base + "/poll"}, {"POST", base + "/hooks/recreate"}, {"POST", base + "/provision"}, {"GET", base + "/accounts"}, {"GET", "/api/v1/users/" + itoa(sani.ID) + "/connectors"}} {
			code, body := e2.raw(r.m, r.p, tok, map[string]any{"all": true})
			if code != 403 || !strings.Contains(body, "admin only") {
				t.Fatalf("%s %s with %s…: %d %s", r.m, r.p, tok[:4], code, body)
			}
		}
	}
	if code, body := e2.raw("POST", "/api/v1/services", memberTok, map[string]any{"type": "x"}); code != 403 || !strings.Contains(body, "admin only") {
		t.Fatalf("reference refusal changed: %d %s", code, body)
	}
}
