package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client is Core's side of the agent's socket.
type Client struct {
	hc    *http.Client
	token string
}

// NewClient returns nil without a socket: this Core installs nothing.
func NewClient(socket, token string) *Client {
	if socket == "" || token == "" {
		return nil
	}
	return &Client{token: token, hc: &http.Client{
		Timeout: time.Minute, // the catalog reads GitHub
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://installer"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("the installer on this server does not answer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return &Error{Status: res.StatusCode, Msg: e.Error}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Error is an answer the agent refused with.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var s Status
	return &s, c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Catalog(ctx context.Context, refresh bool) ([]Release, error) {
	var out struct {
		Apps []Release `json:"apps"`
	}
	path := "/v1/catalog"
	if refresh {
		path += "?refresh=1"
	}
	return out.Apps, c.do(ctx, http.MethodGet, path, nil, &out)
}

func (c *Client) Install(ctx context.Context, req InstallRequest) (*Job, error) {
	var j Job
	return &j, c.do(ctx, http.MethodPost, "/v1/install", req, &j)
}

func (c *Client) Uninstall(ctx context.Context, req UninstallRequest) (*Job, error) {
	var j Job
	return &j, c.do(ctx, http.MethodPost, "/v1/uninstall", req, &j)
}

func (c *Client) Job(ctx context.Context, id string) (*Job, error) {
	var j Job
	return &j, c.do(ctx, http.MethodGet, "/v1/jobs/"+id, nil, &j)
}
