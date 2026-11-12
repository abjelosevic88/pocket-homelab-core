package server

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // decoders for pictures people attach
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Files an app hands out through a tool (Pocket Projects' attachments): an AI
// agent cannot fetch them itself (they sit behind the person's sign-in), so the
// gateway reads the file and gives pictures back as MCP image content the model
// looks at, and text as text. Anything else is described, not sent.
const (
	mcpFileReadLimit  = 25 << 20  // what Core reads from the app
	mcpImageSendLimit = 3_500_000 // bytes per picture sent on (models refuse ~5 MB base64)
	mcpImageMaxSide   = 2000      // longest side sent on, in pixels
	mcpFileTextLimit  = 512 << 10
)

func mcpAppFileResult(op *mcpAppTool, requestID string, res *http.Response) *mcp.CallToolResult {
	raw, err := io.ReadAll(io.LimitReader(res.Body, mcpFileReadLimit+1))
	if err != nil {
		return mcpAppError("Application file could not be read.")
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(res.Header.Get("Content-Type"), ";")[0]))
	info := map[string]any{"mime": ct, "size": len(raw)}
	if _, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		info["file_name"] = params["filename"]
	}
	envelope := map[string]any{"app": op.App, "tool": op.Name, "request_id": requestID, "result": info}
	if ws := strings.ToLower(res.Header.Get("X-Pocket-Workspace")); op.App == "projects" && mcpWorkspaceAddr.MatchString(ws) {
		envelope["workspace"] = ws
	}
	if len(raw) > mcpFileReadLimit {
		info["size"] = nil
		info["shown"] = "nothing"
		info["note"] = "The file is larger than 25 MB, too large to read here. Ask the person what it shows."
		return mcpAppResult(envelope, false)
	}
	switch {
	case strings.HasPrefix(ct, "image/") && ct != "image/svg+xml":
		pic, picMime, w, h, resized, err := mcpImageForModel(raw, ct)
		if err != nil {
			info["shown"] = "nothing"
			info["note"] = "This picture could not be prepared (" + err.Error() + "). Ask the person what it shows."
			return mcpAppResult(envelope, false)
		}
		info["shown"] = "image"
		if w > 0 {
			info["width"], info["height"] = w, h
		}
		if resized != "" {
			info["sent_as"] = resized
		}
		out := mcpAppResult(envelope, false)
		out.Content = append(out.Content, &mcp.ImageContent{Data: pic, MIMEType: picMime})
		return out
	case strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/xml" || ct == "image/svg+xml":
		if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
			info["shown"] = "nothing"
			info["note"] = "The file says it is text but is not readable text."
			return mcpAppResult(envelope, false)
		}
		text := string(raw)
		if len(raw) > mcpFileTextLimit {
			cut := mcpFileTextLimit
			for cut > 0 && !utf8.RuneStart(raw[cut]) {
				cut--
			}
			text = string(raw[:cut])
			info["cut_at"] = cut
		}
		info["shown"] = "text"
		info["text"] = text
		return mcpAppResult(envelope, false)
	default:
		info["shown"] = "nothing"
		info["note"] = "Only pictures and text files can be read here; this is " + ct + ". Ask the person what it holds, or open it in the application."
		return mcpAppResult(envelope, false)
	}
}

// mcpImageForModel returns the picture as the model should get it: unchanged
// when small enough, else scaled down (longest side mcpImageMaxSide) and
// re-encoded as PNG, or JPEG when the PNG is still too large.
func mcpImageForModel(raw []byte, ct string) (out []byte, outMime string, w, h int, resized string, err error) {
	cfg, format, cfgErr := image.DecodeConfig(bytes.NewReader(raw))
	known := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}
	if cfgErr != nil {
		// a format Go cannot read (WebP, HEIC…): send it as it is when it fits
		if ct == "image/webp" && len(raw) <= mcpImageSendLimit {
			return raw, ct, 0, 0, "", nil
		}
		return nil, "", 0, 0, "", fmt.Errorf("%s pictures cannot be read here", strings.TrimPrefix(ct, "image/"))
	}
	w, h = cfg.Width, cfg.Height
	if w*h > 80_000_000 {
		return nil, "", w, h, "", fmt.Errorf("%d×%d pixels is too large", w, h)
	}
	if len(raw) <= mcpImageSendLimit && max(w, h) <= mcpImageMaxSide && known[format] != "" {
		return raw, known[format], w, h, "", nil
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", w, h, "", fmt.Errorf("the picture is damaged")
	}
	for _, side := range []int{mcpImageMaxSide, 1400, 1000} {
		small := mcpShrink(src, side)
		sb := small.Bounds()
		var buf bytes.Buffer
		if png.Encode(&buf, small) == nil && buf.Len() <= mcpImageSendLimit {
			return buf.Bytes(), "image/png", w, h, fmt.Sprintf("png %d×%d", sb.Dx(), sb.Dy()), nil
		}
		buf.Reset()
		if jpeg.Encode(&buf, small, &jpeg.Options{Quality: 85}) == nil && buf.Len() <= mcpImageSendLimit {
			return buf.Bytes(), "image/jpeg", w, h, fmt.Sprintf("jpeg %d×%d", sb.Dx(), sb.Dy()), nil
		}
	}
	return nil, "", w, h, "", fmt.Errorf("it stays too large even when made smaller")
}

// mcpShrink scales src so its longest side is at most side, averaging the
// source pixels under each new pixel (keeps screenshot text readable).
func mcpShrink(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if m := max(sw, sh); m > side {
		dw, dh = max(1, sw*side/m), max(1, sh*side/m)
	}
	rgba, ok := src.(*image.RGBA)
	if !ok || rgba.Bounds().Min != (image.Point{}) {
		rgba = image.NewRGBA(image.Rect(0, 0, sw, sh))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	}
	if dw == sw && dh == sh {
		return rgba
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				row := rgba.Pix[yy*rgba.Stride:]
				for xx := x0; xx < x1; xx++ {
					p := row[xx*4 : xx*4+4]
					r, g, bl, a = r+uint32(p[0]), g+uint32(p[1]), bl+uint32(p[2]), a+uint32(p[3])
					n++
				}
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}
