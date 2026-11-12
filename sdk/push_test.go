package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNotify(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != "/api/v1/push/notify" || r.Header.Get("Authorization") != "Bearer pca_app" || in["email"] != "a@b.c" || in["title"] != "Hi" || in["sound"] != true || in["thread"] != "Backups" {
			t.Errorf("request %s %v", r.URL.Path, in)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": 2, "sent": 1, "gone": 1})
	}))
	defer srv.Close()
	tf := filepath.Join(t.TempDir(), "core.token")
	_ = os.WriteFile(tf, []byte("pca_app\n"), 0o600)
	c := New(srv.URL, Options{TokenFile: tf})
	res, err := c.Notify(t.Context(), "a@b.c", PushMessage{Title: "Hi", Sound: true, Thread: "Backups"})
	if err != nil || res.Devices != 2 || res.Sent != 1 || res.Gone != 1 {
		t.Fatalf("notify: %v %+v", err, res)
	}
}
