package remote

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeCF answers the handful of Cloudflare API calls Core makes.
func fakeCF(t *testing.T) (*httptest.Server, *sync.Mutex, map[string]any) {
	var mu sync.Mutex
	state := map[string]any{"domains": []map[string]string{{"id": "d-old", "hostname": "gone.remote.example.org", "service": "pocket-relay"},
		{"id": "d-keep", "hostname": "pocket.remote.example.org", "service": "pocket-relay"},
		{"id": "d-other", "hostname": "blog.example.org", "service": "someone-else"}}}
	ok := func(w http.ResponseWriter, v any) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": v})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 10000, "message": "Authentication error"}}})
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/accounts/acc/workers/scripts":
			ok(w, []any{})
		case r.Method == "PUT" && r.URL.Path == "/accounts/acc/workers/scripts/pocket-relay":
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			parts := map[string]string{}
			types := map[string]string{}
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				b, _ := io.ReadAll(p)
				name := p.FormName()
				parts[name] = string(b)
				types[name] = p.Header.Get("Content-Type")
			}
			state["upload"], state["types"] = parts, types
			ok(w, map[string]any{})
		case r.Method == "GET" && r.URL.Path == "/accounts/acc/workers/domains":
			ok(w, state["domains"])
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/accounts/acc/workers/domains/"):
			state["deleted"] = append(asList(state["deleted"]), strings.TrimPrefix(r.URL.Path, "/accounts/acc/workers/domains/"))
			ok(w, nil)
		case r.Method == "PUT" && r.URL.Path == "/accounts/acc/workers/domains":
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			state["added"] = append(asList(state["added"]), in["hostname"]+"@"+in["zone_id"]+"→"+in["service"])
			ok(w, in)
		case r.Method == "GET" && r.URL.Path == "/zones":
			if r.URL.Query().Get("name") == "example.org" {
				ok(w, []map[string]string{{"id": "zone1", "name": "example.org"}})
			} else {
				ok(w, []any{})
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &mu, state
}

func asList(v any) []string {
	l, _ := v.([]string)
	return l
}

func TestCloudflareDeployAndDomains(t *testing.T) {
	srv, mu, state := fakeCF(t)
	cf := &Cloudflare{Account: "acc", Token: "tok", Worker: "pocket-relay", API: srv.URL}
	ctx := context.Background()
	if err := (&Cloudflare{Account: "acc", Token: "bad", Worker: "pocket-relay", API: srv.URL}).Verify(ctx); err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("bad token: %v", err)
	}
	if err := cf.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cf.Deploy(ctx, "s3cret", "https://pocket.remote.example.org", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	up := state["upload"].(map[string]string)
	types := state["types"].(map[string]string)
	mu.Unlock()
	var meta struct {
		Main       string           `json:"main_module"`
		Bindings   []map[string]any `json:"bindings"`
		Migrations map[string]any   `json:"migrations"`
	}
	json.Unmarshal([]byte(up["metadata"]), &meta)
	if meta.Main != "worker.js" || meta.Migrations["new_tag"] != "v1" || len(meta.Bindings) != 3 {
		t.Fatalf("metadata %s", up["metadata"])
	}
	for _, b := range meta.Bindings {
		if b["name"] == "BOX_SECRET" && (b["type"] != "secret_text" || b["text"] != "s3cret") {
			t.Fatalf("secret binding %v", b)
		}
	}
	if types["worker.js"] != "application/javascript+module" || !strings.Contains(up["worker.js"], "class Relay") {
		t.Fatalf("worker part %q", types["worker.js"])
	}
	for _, f := range WorkerFiles {
		if types[f+".txt"] != "text/plain" || up[f+".txt"] != string(Asset(f)) {
			t.Fatalf("text module %s missing or wrong (%q)", f, types[f+".txt"])
		}
		if !strings.Contains(up["worker.js"], "./"+f+".txt") {
			t.Fatalf("worker.js does not import %s.txt", f)
		}
	}
	// Second deploy without the migration.
	cf.Deploy(ctx, "s3cret", "https://pocket.remote.example.org", false)
	mu.Lock()
	meta.Migrations = nil
	json.Unmarshal([]byte(state["upload"].(map[string]string)["metadata"]), &meta)
	mu.Unlock()
	if meta.Migrations != nil {
		t.Fatal("migration sent twice")
	}

	if err := cf.SyncDomains(ctx, []string{"pocket.remote.example.org", "money.remote.example.org"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if d := asList(state["deleted"]); len(d) != 1 || d[0] != "d-old" {
		t.Fatalf("deleted %v (must leave other Workers' domains alone)", d)
	}
	if a := asList(state["added"]); len(a) != 1 || a[0] != "money.remote.example.org@zone1→pocket-relay" {
		t.Fatalf("added %v", a)
	}
}
