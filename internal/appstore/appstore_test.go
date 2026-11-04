package appstore

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testCatalog() *Catalog {
	return &Catalog{Generated: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), Source: "github.com/me", Apps: []App{{
		Slug: "notes", Name: "Pocket Notes", Repo: "pocket-homelab-notes", Version: "0.4.5", Tag: "v0.4.5",
		Image: "ghcr.io/me/pocket-homelab-notes", Digest: "sha256:abc", Platforms: []string{"linux/amd64", "linux/arm64"},
		Container: "pocket-notes", Compose: "services: {}\n",
	}}}
}

func TestSignVerify(t *testing.T) {
	priv, pub, err := GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeys([]string{pub})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Sign(testCatalog(), priv)
	if err != nil {
		t.Fatal(err)
	}
	c, id, err := Verify(b, keys)
	if err != nil || id != "test" || c.Find("notes") == nil || c.Find("notes").Ref() != "ghcr.io/me/pocket-homelab-notes:0.4.5@sha256:abc" {
		t.Fatalf("verify: %v %s %+v", err, id, c)
	}

	// Any change to the catalog's bytes breaks the signature.
	bad := bytes.Replace(b, []byte("0.4.5"), []byte("0.4.6"), 1)
	if _, _, err := Verify(bad, keys); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered catalog accepted: %v", err)
	}
	// An unknown key is refused.
	_, other, _ := GenerateKey("other")
	ok, _ := ParseKeys([]string{other})
	if _, _, err := Verify(b, ok); err == nil || !strings.Contains(err.Error(), "does not trust") {
		t.Fatalf("unknown key accepted: %v", err)
	}
	// Same id, different key.
	_, imposter, _ := GenerateKey("test")
	ik, _ := ParseKeys([]string{imposter})
	if _, _, err := Verify(b, ik); err == nil {
		t.Fatal("imposter key accepted")
	}
}

func TestOfficialKeysParse(t *testing.T) {
	if _, err := ParseKeys(OfficialKeys); err != nil {
		t.Fatal(err)
	}
}

func TestFetch(t *testing.T) {
	priv, pub, _ := GenerateKey("k")
	keys, _ := ParseKeys([]string{pub})
	b, _ := Sign(testCatalog(), priv)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/apps.json", http.StatusFound)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	c, err := Fetch(context.Background(), srv.Client(), srv.URL+"/redirect", keys)
	if err != nil || len(c.Apps) != 1 {
		t.Fatalf("fetch: %v", err)
	}
}

func TestSupportsAndSame(t *testing.T) {
	a := testCatalog().Apps[0]
	if !a.Supports("linux", "arm64") || a.Supports("linux", "arm") {
		t.Fatal("platform check")
	}
	a.Platforms = nil
	if !a.Supports("linux", "riscv64") {
		t.Fatal("unknown platforms should be allowed")
	}
	x, y := testCatalog(), testCatalog()
	y.Generated = time.Now()
	if !SameApps(x, y) {
		t.Fatal("generated time should not matter")
	}
	y.Apps[0].Version = "0.4.6"
	if SameApps(x, y) {
		t.Fatal("version change missed")
	}
}
