package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterStoresToken(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/apps/register" {
			t.Fatalf("path %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if calls == 1 && auth != "Bearer pcreg_key" {
			t.Fatalf("first call should use the registration key, got %q", auth)
		}
		if calls == 2 && auth != "Bearer pca_minted" {
			t.Fatalf("second call should use the app token, got %q", auth)
		}
		out := map[string]any{"app": map[string]any{"slug": "demo"}, "core": map[string]any{"base_url": "x"}}
		if calls == 1 {
			out["token"] = "pca_minted"
			out["oidc"] = map[string]any{"client_id": "pocket-demo", "client_secret": "pcs_s", "issuer": "http://core"}
		} else {
			out["oidc"] = map[string]any{"client_id": "pocket-demo", "issuer": "http://core"}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	tf := filepath.Join(t.TempDir(), "core.token")
	c := New(srv.URL, Options{TokenFile: tf})
	reg, err := c.Register(context.Background(), "pcreg_key", Manifest{Slug: "demo", Name: "Demo", URL: "http://demo"})
	if err != nil || reg.Token != "pca_minted" {
		t.Fatalf("register: %v %+v", err, reg)
	}
	if b, _ := os.ReadFile(tf); string(b) != "pca_minted\n" {
		t.Fatalf("token file: %q", b)
	}
	if _, err := c.Register(context.Background(), "", Manifest{Slug: "demo", Name: "Demo", URL: "http://demo"}); err != nil {
		t.Fatal(err)
	}
	c2 := New(srv.URL, Options{TokenFile: tf})
	if c2.Token() != "pca_minted" {
		t.Fatalf("token not reloaded from file")
	}
	if o := c2.OIDC(); o.ClientID != "pocket-demo" || o.ClientSecret != "pcs_s" || o.Issuer != "http://core" {
		t.Fatalf("oidc client not persisted across registrations: %+v", o)
	}
}

func TestNotConfigured(t *testing.T) {
	c := New("", Options{})
	if err := c.Publish(context.Background(), "x.y", "", nil); err != ErrNotConfigured {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}
