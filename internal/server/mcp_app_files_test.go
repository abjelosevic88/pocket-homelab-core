package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMCPAppAttachmentFiles(t *testing.T) {
	f := newOAuthFixture(t)
	op := &mcpAppTool{App: "projects", Name: "pocket_projects_attachment", Method: "GET", Path: "/attachments/{id}/file", FileResponse: true}
	serve := func(ct, disp string, body []byte) *mcp.CallToolResult {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") != "*/*" {
				t.Errorf("accept %q", r.Header.Get("Accept"))
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Disposition", disp)
			w.Header().Set("X-Pocket-Workspace", "projects.athena.example")
			w.Write(body)
		}))
		defer up.Close()
		r := f.s.requestMCPApp(context.Background(), op, up.URL, "tok", "", nil, "")
		return addMCPAppLinks(r, "https://projects.example.com", op, map[string]any{"id": "7"})
	}
	picOf := func(r *mcp.CallToolResult) *mcp.ImageContent {
		for _, c := range r.Content {
			if im, ok := c.(*mcp.ImageContent); ok {
				return im
			}
		}
		return nil
	}

	small := testPNG(t, 300, 200)
	r := serve("image/png", `inline; filename*=UTF-8''shot%201.png`, small)
	im := picOf(r)
	if r.IsError || im == nil || !bytes.Equal(im.Data, small) || im.MIMEType != "image/png" {
		t.Fatalf("small picture not passed through: %+v", r)
	}
	env := r.StructuredContent.(map[string]any)
	info := env["result"].(map[string]any)
	if info["file_name"] != "shot 1.png" || info["width"] != 300 || env["workspace"] != "projects.athena.example" || env["app_url"] != "https://projects.example.com/r/projects.athena.example/" {
		t.Fatalf("envelope %+v", env)
	}

	big := testPNG(t, 3000, 1000)
	r = serve("image/png", "", big)
	im = picOf(r)
	if r.IsError || im == nil {
		t.Fatalf("big picture: %+v", r)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(im.Data))
	if err != nil || cfg.Width != 1568 || cfg.Height != 522 || len(im.Data) > mcpImageSendLimit {
		t.Fatalf("not scaled: %v %d×%d %d bytes", err, cfg.Width, cfg.Height, len(im.Data))
	}

	r = serve("application/pdf", "", []byte("%PDF-1.4"))
	if r.IsError || picOf(r) != nil || r.StructuredContent.(map[string]any)["result"].(map[string]any)["shown"] != "nothing" {
		t.Fatalf("pdf: %+v", r)
	}
	r = serve("text/plain; charset=utf-8", "", []byte("log line"))
	if r.StructuredContent.(map[string]any)["result"].(map[string]any)["text"] != "log line" {
		t.Fatalf("text: %+v", r)
	}
	r = serve("image/png", "", []byte("not a png"))
	if picOf(r) != nil || r.StructuredContent.(map[string]any)["result"].(map[string]any)["shown"] != "nothing" {
		t.Fatalf("broken picture: %+v", r)
	}
}
