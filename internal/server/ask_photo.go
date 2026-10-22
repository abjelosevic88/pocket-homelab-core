package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"mime/multipart"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Photo bytes arrive from the authenticated browser after the person presses
// Ask. Never fetch a supplied image URL or forward a Photos/cloud credential.
func (s *Server) prepareAskPhoto(ctx context.Context, grant, app string, raw []byte) ([]byte, func(), int, error) {
	var in struct {
		Text  string `json:"text"`
		Image *struct {
			Name    string `json:"name"`
			DataURL string `json:"data_url"`
		} `json:"image"`
		UploadIDs []string `json:"upload_ids"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil, nil, 400, errors.New("invalid question")
	}
	if in.Image == nil {
		return raw, nil, 0, nil
	}
	if app != "photos" || len(in.UploadIDs) != 0 || strings.TrimSpace(in.Text) == "" || len(in.Text) > 30000 {
		return nil, nil, 400, errors.New("Choose a photo and write a question")
	}
	prefix, encoded, ok := strings.Cut(in.Image.DataURL, ",")
	if !ok || (prefix != "data:image/jpeg;base64" && prefix != "data:image/png;base64") || len(encoded) > base64.StdEncoding.EncodedLen(2<<20) {
		return nil, nil, 400, errors.New("Attach a JPEG or PNG preview up to 2 MB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 2<<20 {
		return nil, nil, 400, errors.New("Invalid photo preview")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || prefix != "data:image/"+format+";base64" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return nil, nil, 400, errors.New("Invalid or oversized photo preview")
	}
	name := path.Base(strings.ReplaceAll(in.Image.Name, "\\", "/"))
	if name == "" || name == "." || len(name) > 240 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		name = "photo." + format
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", name)
	if err != nil {
		return nil, nil, 400, errors.New("Invalid photo name")
	}
	_, _ = file.Write(data)
	_ = form.Close()
	code, response, err := s.askAIRequest(ctx, grant, "POST", "/api/v1/uploads", form.FormDataContentType(), body.Bytes())
	var uploads []struct {
		ID string `json:"id"`
	}
	if err != nil || code != 201 || json.Unmarshal(response, &uploads) != nil || len(uploads) != 1 {
		return nil, nil, 502, errors.New("Pocket AI could not attach the photo. Try again.")
	}
	id, err := uuid.Parse(uploads[0].ID)
	if err != nil {
		return nil, nil, 502, errors.New("Pocket AI returned an invalid attachment")
	}
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(raw, &payload)
	delete(payload, "image")
	payload["upload_ids"], _ = json.Marshal([]string{id.String()})
	out, _ := json.Marshal(payload)
	cleanup := func() {
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _, _ = s.askAIRequest(clean, grant, "DELETE", "/api/v1/uploads/"+id.String(), "application/json", nil)
	}
	return out, cleanup, 0, nil
}
