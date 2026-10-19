package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/connectors"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

type pushPersonT struct {
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
	Workflows []struct {
		Trigger          string `json:"trigger"`
		RemoteWorkflowID string `json:"remote_workflow_id"`
	} `json:"workflows"`
	Received       int64  `json:"received"`
	Published      int64  `json:"published"`
	Dropped        int64  `json:"dropped"`
	LastDrop       string `json:"last_drop"`
	LastReceivedAt string `json:"last_received_at"`
}

type pushViewT struct {
	EventsEnabled bool   `json:"events_enabled"`
	LastError     string `json:"last_error"`
	Push          *struct {
		PerUser        bool          `json:"per_user"`
		Received       int64         `json:"received"`
		Published      int64         `json:"published"`
		Dropped        int64         `json:"dropped"`
		LastReceivedAt string        `json:"last_received_at"`
		People         []pushPersonT `json:"people"`
	} `json:"push"`
}

func (v pushViewT) person(email string) pushPersonT {
	for _, p := range v.Push.People {
		if p.Email == email {
			return p
		}
	}
	return pushPersonT{}
}

// testImmichEvents runs inside TestImmichConnector once accounts exist:
// admin@b.c linked (the box-wide key is theirs), sani@example.com with a
// key Core minted, own@example.com with a hand-set key, existing@example.com
// linked without any key of theirs.
func testImmichEvents(t *testing.T, e *env, im *fakeImmich, base string, svID int64, call func(string, string, any, any) int, remote map[string]string) {
	ctx := context.Background()
	var v pushViewT
	view := func() pushViewT {
		t.Helper()
		var out pushViewT
		if c := call("GET", base, nil, &out); c != 200 || out.Push == nil {
			t.Fatalf("connector view %d %+v", c, out)
		}
		return out
	}
	localHooks := func() []store.ConnectorHook {
		hs, err := e.st.ListConnectorHooks(ctx, svID)
		if err != nil {
			t.Fatal(err)
		}
		return hs
	}

	// Events ON creates two workflows per connected person with their own key.
	if c := call("PUT", base, map[string]any{"events_enabled": true}, &v); c != 200 || !v.EventsEnabled || v.Push == nil || !v.Push.PerUser {
		t.Fatalf("events on %d %+v", c, v)
	}
	if v.LastError != "" {
		t.Fatalf("events on error: %s", v.LastError)
	}
	secrets := map[string]string{} // remote workflow id → header secret
	for email, uid := range remote {
		wfs := im.workflowsOf(uid)
		if len(wfs) != 2 {
			t.Fatalf("%s has %d workflows, want 2", email, len(wfs))
		}
		for trig, name := range map[string]string{"AssetCreate": "Pocket Core · uploads", "AssetMetadataExtraction": "Pocket Core · details"} {
			wf, ok := wfs[trig]
			if !ok || wf.Name != name || !wf.Enabled || wf.Logging || len(wf.Steps) != 1 {
				t.Fatalf("%s %s workflow %+v", email, trig, wf)
			}
			st := wf.Steps[0]
			u, _ := st.Config["url"].(string)
			if st.Method != connectors.ImmichWebhookMethod || !strings.HasPrefix(u, "http://core.test/api/v1/connectors/hooks/") || st.Config["method"] != "POST" ||
				st.Config["headerName"] != "X-Pocket-Hook" || len(st.Config["headerValue"].(string)) < 32 {
				t.Fatalf("%s %s step %+v", email, trig, st)
			}
			secrets[wf.ID] = st.Config["headerValue"].(string)
		}
		if p := v.person(email); p.State != "ok" || len(p.Workflows) != 2 {
			t.Fatalf("%s status %+v", email, p)
		}
	}
	if p := v.person("existing@example.com"); p.State != "needs_key" {
		t.Fatalf("existing (linked, no key) should need a key: %+v", p)
	}
	if n, _ := im.workflowCount(); n != 6 || len(localHooks()) != 6 {
		t.Fatalf("workflows %d, hooks %d", n, len(localHooks()))
	}
	// Secrets live only in Immich: Core keeps hashes.
	files, _ := filepath.Glob(filepath.Join(e.dir, "pocket-core.db*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, sec := range secrets {
			if bytes.Contains(b, []byte(sec)) {
				t.Fatalf("hook secret stored in %s", f)
			}
		}
	}

	// ON again and a manual check change nothing in Immich.
	_, creates := im.workflowCount()
	call("PUT", base, map[string]any{"events_enabled": true}, &v)
	var poll struct {
		OK       bool   `json:"ok"`
		Events   int    `json:"events"`
		Baseline bool   `json:"baseline"`
		Err      string `json:"error"`
	}
	call("POST", base+"/poll", nil, &poll)
	if !poll.OK || !poll.Baseline {
		t.Fatalf("first check %+v", poll)
	}
	if n, c2 := im.workflowCount(); n != 6 || c2 != creates {
		t.Fatalf("not idempotent: %d workflows, %d creates (was %d)", n, c2, creates)
	}

	// Deliveries.
	sani := im.workflowsOf(remote["sani@example.com"])
	deliver := func(wf fakeWorkflow, secret string, body any) (int, string) {
		t.Helper()
		u := strings.TrimPrefix(wf.Steps[0].Config["url"].(string), "http://core.test")
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", e.srv.URL+u, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Pocket-Hook", secret)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(res.Body)
		return res.StatusCode, buf.String()
	}
	asset := func(id, owner string, set map[string]any) map[string]any {
		a := map[string]any{"id": id, "ownerId": owner, "type": "IMAGE", "originalFileName": "IMG_0001.HEIC", "originalPath": "/data/upload/u/IMG_0001.HEIC",
			"checksum": "c2VjcmV0LWNoZWNrc3VtLWJsb2I=", "fileCreatedAt": "2026-10-19T08:00:00.000Z", "localDateTime": "2026-10-19T10:00:00.000Z",
			"createdAt": time.Now().UTC().Format(time.RFC3339Nano), "visibility": "timeline", "isFavorite": false, "duration": "0:00:00.00000",
			"livePhotoVideoId": nil, "deletedAt": nil, "exifInfo": nil, "tags": []any{}}
		for k, val := range set {
			a[k] = val
		}
		return map[string]any{"type": "AssetV1", "trigger": map[bool]string{true: "AssetMetadataExtraction", false: "AssetCreate"}[a["exifInfo"] != nil], "data": map[string]any{"asset": a}}
	}
	events := func(topic string) []store.Event {
		t.Helper()
		var evs []store.Event
		call("GET", "/api/v1/events?topic="+topic, nil, &evs)
		return evs
	}
	up := sani["AssetCreate"]
	upSecret := secrets[up.ID]
	if c, body := deliver(up, upSecret, asset("a-1", remote["sani@example.com"], nil)); c != 202 || !strings.Contains(body, `"published":true`) {
		t.Fatalf("delivery %d %s", c, body)
	}
	evs := events("immich.asset_created")
	if len(evs) != 1 || evs[0].Source != "connector:immich" || evs[0].Subject != "asset:a-1" {
		t.Fatalf("asset_created %+v", evs)
	}
	var p map[string]any
	_ = json.Unmarshal(evs[0].Payload, &p)
	if p["user_email"] != "sani@example.com" || p["asset_id"] != "a-1" || p["type"] != "image" || p["original_file_name"] != "IMG_0001.HEIC" ||
		p["immich_user_id"] != remote["sani@example.com"] || p["url"] != "https://photos.example/photos/a-1" {
		t.Fatalf("asset_created payload %v", p)
	}
	for _, k := range []string{"originalPath", "original_path", "checksum", "duration"} {
		if _, has := p[k]; has {
			t.Fatalf("payload leaks %s: %v", k, p)
		}
	}
	if strings.Contains(string(evs[0].Payload), "/data/upload") || strings.Contains(string(evs[0].Payload), "c2VjcmV0") {
		t.Fatalf("payload leaks a path or a blob: %s", evs[0].Payload)
	}

	// Wrong secret, no secret, unknown hook: 404 and nothing published.
	if c, _ := deliver(up, "wrong-secret", asset("a-2", remote["sani@example.com"], nil)); c != 404 {
		t.Fatalf("wrong secret %d", c)
	}
	if c, _ := deliver(up, "", asset("a-2", remote["sani@example.com"], nil)); c != 404 {
		t.Fatalf("no secret %d", c)
	}
	ghost := up
	ghost.Steps = append(ghost.Steps[:0:0], up.Steps...)
	ghost.Steps[0].Config = map[string]any{"url": "http://core.test/api/v1/connectors/hooks/nope"}
	if c, _ := deliver(ghost, upSecret, asset("a-2", remote["sani@example.com"], nil)); c != 404 {
		t.Fatalf("unknown hook %d", c)
	}
	// A duplicate delivery is not published twice.
	if c, body := deliver(up, upSecret, asset("a-1", remote["sani@example.com"], nil)); c != 202 || !strings.Contains(body, "duplicate") {
		t.Fatalf("duplicate %d %s", c, body)
	}
	// An asset whose owner is not this hook's person (or not a Core user) is dropped.
	if c, body := deliver(up, upSecret, asset("a-3", "u-stranger", nil)); c != 202 || !strings.Contains(body, "not this hook") {
		t.Fatalf("stranger %d %s", c, body)
	}
	// The video half of a live photo is not an upload of its own.
	if c, body := deliver(up, upSecret, asset("a-4", remote["sani@example.com"], map[string]any{"visibility": "hidden", "type": "VIDEO"})); c != 202 || !strings.Contains(body, "hidden") {
		t.Fatalf("hidden %d %s", c, body)
	}
	if evs := events("immich.asset_created"); len(evs) != 1 {
		t.Fatalf("published %d asset_created, want 1", len(evs))
	}
	// Garbage is refused.
	if c, _ := deliver(up, upSecret, map[string]any{"hello": "world"}); c != 400 {
		t.Fatalf("garbage %d", c)
	}

	// Too big is refused before reading it all.
	if c, _ := deliver(up, upSecret, map[string]any{"pad": strings.Repeat("x", 300<<10)}); c != 413 {
		t.Fatalf("oversized %d", c)
	}

	// Details: place and camera, never coordinates.
	det := sani["AssetMetadataExtraction"]
	exif := map[string]any{"make": "Apple", "model": "iPhone 16 Pro", "dateTimeOriginal": "2026-10-19T08:00:00+00:00", "timeZone": "Europe/Sarajevo",
		"city": "Sarajevo", "state": "Federation of Bosnia and Herzegovina", "country": "Bosnia and Herzegovina", "latitude": 43.856259, "longitude": 18.413076,
		"exifImageWidth": 4032, "exifImageHeight": 3024}
	if c, body := deliver(det, secrets[det.ID], asset("a-1", remote["sani@example.com"], map[string]any{"exifInfo": exif})); c != 202 || !strings.Contains(body, `"published":true`) {
		t.Fatalf("details %d %s", c, body)
	}
	evs = events("immich.asset_processed")
	if len(evs) != 1 || evs[0].Subject != "asset:a-1" {
		t.Fatalf("asset_processed %+v", evs)
	}
	p = nil
	_ = json.Unmarshal(evs[0].Payload, &p)
	if p["user_email"] != "sani@example.com" || p["city"] != "Sarajevo" || p["country"] != "Bosnia and Herzegovina" || p["camera_make"] != "Apple" ||
		p["camera_model"] != "iPhone 16 Pro" || p["taken_at"] != "2026-10-19T08:00:00+00:00" || p["width"] != float64(4032) {
		t.Fatalf("asset_processed payload %v", p)
	}
	raw := string(evs[0].Payload)
	if strings.Contains(raw, "latitude") || strings.Contains(raw, "longitude") || strings.Contains(raw, "43.85") || strings.Contains(raw, "18.41") {
		t.Fatalf("coordinates on the bus: %s", raw)
	}
	// A metadata re-scan of an old asset is not news.
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if c, body := deliver(det, secrets[det.ID], asset("a-old", remote["sani@example.com"], map[string]any{"exifInfo": exif, "createdAt": old})); c != 202 || !strings.Contains(body, "re-scan") {
		t.Fatalf("re-scan %d %s", c, body)
	}

	// Status shows what arrived.
	v = view()
	sp := v.person("sani@example.com")
	if sp.Published != 2 || sp.Dropped != 5 || sp.Received != 7 || sp.LastReceivedAt == "" || v.Push.Published != 2 {
		t.Fatalf("counters %+v (total %d)", sp, v.Push.Published)
	}

	// Heal: a workflow deleted in Immich is re-created by the scheduled check, with a new secret.
	im.mu.Lock()
	delete(im.workflows, up.ID)
	im.users = append(im.users, fakeImmichUser{ID: "u-new", Email: "New@Example.com", Name: "New"})
	im.mu.Unlock()
	svc, _ := e.st.GetService(ctx, svID)
	c, _ := connectors.Get("immich")
	e.s.checkConnector(ctx, svc, c, true)
	healed := im.workflowsOf(remote["sani@example.com"])["AssetCreate"]
	if healed.ID == "" || healed.ID == up.ID {
		t.Fatalf("not healed: %+v", healed)
	}
	if healed.Steps[0].Config["url"] != up.Steps[0].Config["url"] {
		t.Fatalf("healed hook should keep its URL")
	}
	if c, _ := deliver(healed, upSecret, asset("a-5", remote["sani@example.com"], nil)); c != 404 {
		t.Fatalf("old secret still accepted: %d", c)
	}
	if c, _ := deliver(healed, healed.Steps[0].Config["headerValue"].(string), asset("a-5", remote["sani@example.com"], nil)); c != 202 {
		t.Fatalf("new secret refused: %d", c)
	}
	// …and new Immich accounts are still events.
	if evs := events("immich.user_created"); len(evs) != 1 || !strings.Contains(string(evs[0].Payload), `"user_email":"new@example.com"`) {
		t.Fatalf("user_created %+v", evs)
	}
	if evs := events("immich.assets_added"); len(evs) != 0 {
		t.Fatalf("count-diff events are gone: %+v", evs)
	}

	// A person provisioned while events are on gets their workflows; leaving the audience removes them.
	var nina store.User
	call("POST", "/api/v1/users", map[string]string{"email": "nina@example.com", "name": "Nina", "password": "password123"}, &nina)
	var pr struct {
		Results []provisionResult `json:"results"`
	}
	call("POST", base+"/provision", map[string]any{"user_id": nina.ID}, &pr)
	if len(pr.Results) != 1 || pr.Results[0].Status != "ok" {
		t.Fatalf("provision nina %+v", pr)
	}
	ninaIU, _ := im.userByEmail("nina@example.com")
	waitFor(t, "nina's workflows", func() bool { return len(im.workflowsOf(ninaIU.ID)) == 2 })
	var users []store.User
	call("GET", "/api/v1/users", nil, &users)
	keep := []int64{}
	for _, u := range users {
		if u.Email != "nina@example.com" {
			keep = append(keep, u.ID)
		}
	}
	call("PUT", "/api/v1/services/"+itoa(svID), map[string]any{"audience": "users", "audience_users": keep}, nil)
	waitFor(t, "nina's workflows deleted", func() bool {
		hs, _ := e.st.ListUserConnectorHooks(ctx, nina.ID)
		return len(im.workflowsOf(ninaIU.ID)) == 0 && len(hs) == 0
	})
	if len(im.workflowsOf(remote["sani@example.com"])) != 2 {
		t.Fatalf("someone still in the audience lost their workflows")
	}
	call("PUT", "/api/v1/services/"+itoa(svID), map[string]any{"audience": "all"}, nil)
	waitFor(t, "nina's workflows back", func() bool { return len(im.workflowsOf(ninaIU.ID)) == 2 })
	// A disabled person loses them too.
	if c := call("PUT", "/api/v1/users/"+itoa(nina.ID), map[string]any{"disabled": true}, nil); c != 200 {
		t.Fatalf("disable nina %d", c)
	}
	waitFor(t, "disabled nina's workflows deleted", func() bool { return len(im.workflowsOf(ninaIU.ID)) == 0 })
	call("PUT", "/api/v1/users/"+itoa(nina.ID), map[string]any{"disabled": false}, nil)
	waitFor(t, "nina's workflows back again", func() bool { return len(im.workflowsOf(ninaIU.ID)) == 2 })
	// Deleting the person deletes them in Immich first, while their key still exists.
	if c := call("DELETE", "/api/v1/users/"+itoa(nina.ID), nil, nil); c != 204 {
		t.Fatalf("delete nina %d", c)
	}
	if n := len(im.workflowsOf(ninaIU.ID)); n != 0 {
		t.Fatalf("deleted person kept %d workflows", n)
	}

	// Re-create: every workflow made again with new ids.
	before := map[string]bool{}
	im.mu.Lock()
	for id := range im.workflows {
		before[id] = true
	}
	im.mu.Unlock()
	var rec struct {
		OK  bool   `json:"ok"`
		Err string `json:"error"`
	}
	call("POST", base+"/hooks/recreate", nil, &rec)
	if !rec.OK {
		t.Fatalf("recreate %+v", rec)
	}
	im.mu.Lock()
	if len(im.workflows) != 6 {
		t.Fatalf("after recreate %d workflows", len(im.workflows))
	}
	for id := range im.workflows {
		if before[id] {
			t.Fatalf("workflow %s survived a re-create", id)
		}
	}
	im.mu.Unlock()

	// Events OFF deletes every workflow in Immich and every hook in Core.
	saniNow := im.workflowsOf(remote["sani@example.com"])["AssetCreate"]
	if c := call("PUT", base, map[string]any{"events_enabled": false}, &v); c != 200 || v.EventsEnabled {
		t.Fatalf("events off %d", c)
	}
	if n, _ := im.workflowCount(); n != 0 || len(localHooks()) != 0 {
		t.Fatalf("after off: %d workflows, %d hooks", n, len(localHooks()))
	}
	if c, _ := deliver(saniNow, saniNow.Steps[0].Config["headerValue"].(string), asset("a-6", remote["sani@example.com"], nil)); c != 404 {
		t.Fatalf("delivery after off %d", c)
	}
	// The scheduled round leaves a switched-off connector alone.
	e.s.checkConnector(ctx, svc, c, false)
	if n, _ := im.workflowCount(); n != 0 {
		t.Fatalf("workflows came back while off: %d", n)
	}
}
