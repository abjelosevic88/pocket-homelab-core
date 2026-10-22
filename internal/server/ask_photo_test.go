package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAskPhotoAttachment(t *testing.T) {
	f := newOAuthFixture(t)
	f.s.cfg.MCPAppToolsEnabled = true
	var photo bytes.Buffer
	if err := png.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(photo.Bytes())
	const uploadID = "00000000-0000-4000-8000-000000000001"
	uploads, threads, cleanups := 0, 0, 0
	failThread := false
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, err := f.st.GetMCPSecret(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "app", "")
		if err != nil || secret.UserID != f.user.ID {
			t.Error("photo used the wrong account")
			w.WriteHeader(401)
			return
		}
		var binding mcpAuthorization
		_ = json.Unmarshal(secret.Data, &binding)
		if binding.Target != "ai" {
			t.Error("photo credential not bound to AI")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/ask/connect":
			w.WriteHeader(204)
		case r.Method == "POST" && r.URL.Path == "/api/v1/uploads":
			uploads++
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			defer r.MultipartForm.RemoveAll()
			data, _ := io.ReadAll(file)
			if !bytes.Equal(data, photo.Bytes()) || header.Filename != "screenshot.png" {
				t.Error("photo bytes/name lost")
			}
			w.WriteHeader(201)
			io.WriteString(w, `[{"id":"`+uploadID+`"}]`)
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/uploads/"+uploadID:
			cleanups++
			w.WriteHeader(204)
		case r.URL.Path == "/api/v1/ask/threads":
			threads++
			var payload map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if _, ok := payload["image"]; ok {
				t.Error("raw photo leaked into thread metadata")
			}
			var ids []string
			_ = json.Unmarshal(payload["upload_ids"], &ids)
			if len(ids) != 1 || ids[0] != uploadID {
				t.Error("missing real image attachment")
			}
			if failThread {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid question"}`)
				return
			}
			w.WriteHeader(201)
			io.WriteString(w, `{"id":"00000000-0000-4000-8000-000000000002"}`)
		default:
			t.Error("unexpected upstream path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer ai.Close()
	f.app("ai", ai.URL, "all")
	f.app("photos", "https://photos.test", "all")
	if res, _ := f.request("POST", "/api/v1/ask/connect", `{"approved":true}`, oauthTestOrigin, true); res.StatusCode != 204 {
		t.Fatal("connect failed")
	}
	request := func(app, url, data string, origin string, cookie bool) int {
		raw, _ := json.Marshal(map[string]any{"text": "Read this screenshot", "source": map[string]string{"app": app, "url": url, "title": "Screenshot"}, "image": map[string]string{"name": "screenshot.png", "data_url": data}})
		res, _ := f.request("POST", "/api/v1/ask/threads", string(raw), origin, cookie)
		return res.StatusCode
	}
	for _, tc := range []struct {
		app, url, data, origin string
		cookie                 bool
		want                   int
	}{
		{"photos", "https://photos.test/photo/1", dataURL, oauthTestOrigin, false, 401},
		{"photos", "https://photos.test/photo/1", dataURL, "https://evil.test", true, 403},
		{"photos", "https://evil.test/photo/1", dataURL, oauthTestOrigin, true, 400},
		{"other", "https://photos.test/photo/1", dataURL, oauthTestOrigin, true, 403},
		{"ai", ai.URL, dataURL, oauthTestOrigin, true, 400},
		{"photos", "https://photos.test/photo/1", "https://private.test/photo", oauthTestOrigin, true, 400},
		{"photos", "https://photos.test/photo/1", "data:image/png;base64,bm90LWFuLWltYWdl", oauthTestOrigin, true, 400},
		{"photos", "https://photos.test/photo/1", strings.Replace(dataURL, "image/png", "image/jpeg", 1), oauthTestOrigin, true, 400},
		{"photos", "https://photos.test/photo/1", "data:image/png;base64," + strings.Repeat("A", 3<<20), oauthTestOrigin, true, 413},
	} {
		if got := request(tc.app, tc.url, tc.data, tc.origin, tc.cookie); got != tc.want {
			t.Fatalf("photo rejection: got %d want %d", got, tc.want)
		}
	}
	if uploads != 0 || threads != 0 {
		t.Fatal("invalid photo reached AI")
	}
	if got := request("photos", "https://photos.test/photo/1", dataURL, oauthTestOrigin, true); got != 201 {
		t.Fatalf("valid photo: %d", got)
	}
	if uploads != 1 || threads != 1 || cleanups != 0 {
		t.Fatal("photo lifecycle failed")
	}
	failThread = true
	if got := request("photos", "https://photos.test/photo/1", dataURL, oauthTestOrigin, true); got != 400 {
		t.Fatalf("failed thread: %d", got)
	}
	if uploads != 2 || threads != 2 || cleanups != 1 {
		t.Fatal("unused photo was not cleaned up")
	}
}
