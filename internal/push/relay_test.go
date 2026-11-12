package push

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestEnrollBatchAndStatus(t *testing.T) {
	var enrolls, sends, statuses atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/enroll":
			enrolls.Add(1)
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("x7k2m9:names-secret")) {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "pps_enrolled", "sender": "x7k2m9"})
		case "/v1/status":
			statuses.Add(1)
			if r.Header.Get("Authorization") != "Bearer pps_enrolled" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sender": "x7k2m9", "apple": "missing"})
		case "/v1/send":
			sends.Add(1)
			var in struct{ Messages []Out }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if len(in.Messages) > BatchSize {
				w.WriteHeader(413)
				return
			}
			res := make([]Result, len(in.Messages))
			for i := range res {
				res[i] = Result{Status: Sent}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	acct := filepath.Join(dir, "account.json")
	_ = os.WriteFile(acct, []byte(`{"name":"x7k2m9","token":"names-secret"}`), 0o600)
	r := NewRelay(Config{URL: srv.URL + "/", NamesAccount: acct, DataDir: dir})

	if st := r.Status(t.Context()); st != StatusNoAppleKey {
		t.Fatalf("status %q", st)
	}
	fi, err := os.Stat(filepath.Join(dir, "push-relay.token"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", err, fi)
	}
	// Cached for a minute.
	r.Status(t.Context())
	if statuses.Load() != 1 || enrolls.Load() != 1 {
		t.Fatalf("status calls %d, enrolls %d", statuses.Load(), enrolls.Load())
	}

	msgs := make([]Out, 250)
	res, err := r.Send(t.Context(), msgs)
	if err != nil || len(res) != 250 || sends.Load() != 3 {
		t.Fatalf("send: %v results=%d calls=%d", err, len(res), sends.Load())
	}
	if r.Status(t.Context()) != StatusReady {
		t.Fatal("a sent message didn't mark the relay ready")
	}

	// A restarted Core reads the enrolled token back without enrolling again.
	r2 := NewRelay(Config{URL: srv.URL, NamesAccount: acct, DataDir: dir})
	if st := r2.Status(t.Context()); st != StatusNoAppleKey || enrolls.Load() != 1 {
		t.Fatalf("restart: %q enrolls=%d", st, enrolls.Load())
	}
}

func TestNoTokenNoCalls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	r := NewRelay(Config{URL: srv.URL, DataDir: t.TempDir()})
	if r.Status(t.Context()) != StatusNoRelay {
		t.Fatal("status without token")
	}
	res, err := r.Send(t.Context(), make([]Out, 2))
	if err != nil || len(res) != 2 || res[0].Status != Failed || res[0].Reason != ReasonNoRelay || calls.Load() != 0 {
		t.Fatalf("send without token: %v %+v calls=%d", err, res, calls.Load())
	}
}
