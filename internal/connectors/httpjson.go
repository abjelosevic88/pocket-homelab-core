package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// appError is a non-2xx answer from an app.
type appError struct {
	App    string
	Status int
	Msg    string
}

func (e *appError) Error() string {
	return fmt.Sprintf("%s answered HTTP %d: %s", e.App, e.Status, e.Msg)
}

func appStatus(err error) int {
	var ae *appError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// doJSON makes one request to the app at t.BaseURL+path and decodes a JSON
// answer into out (raw bytes when out is *[]byte). Secrets never appear in
// errors. It returns the answer's headers.
func doJSON(ctx context.Context, t Target, app, method, path string, hdr map[string]string, body, out any, secrets ...string) (http.Header, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(t.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := t.HTTP.Do(req)
	if err != nil {
		return nil, errors.New(redact(err.Error(), secrets...))
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return res.Header, &appError{App: app, Status: res.StatusCode, Msg: redact(msg, secrets...)}
	}
	if b, ok := out.(*[]byte); ok {
		*b = raw
		return res.Header, nil
	}
	if out == nil || len(raw) == 0 {
		return res.Header, nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return res.Header, fmt.Errorf("%s %s %s: unexpected answer: %w", app, method, path, err)
	}
	return res.Header, nil
}
