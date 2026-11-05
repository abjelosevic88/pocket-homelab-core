package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Cloudflare uploads the relay Worker to the person's own account and keeps
// one Worker custom domain per reachable app. The token needs Workers
// Scripts:Edit (account) and Workers Routes:Edit, DNS:Edit, Zone:Read (zone).
type Cloudflare struct {
	Account string
	Token   string
	Worker  string
	API     string // override for tests
	HTTP    *http.Client
}

// WorkerFiles are the modules uploaded besides worker.js.
var WorkerFiles = append([]string{"shell.html"}, ShellFiles...)

// WorkerHash identifies the asset version, so Core can tell when to re-upload.
func WorkerHash() string {
	h := sha256.New()
	for _, f := range append([]string{"worker.js"}, WorkerFiles...) {
		h.Write([]byte(f))
		h.Write(Asset(f))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type cfResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (cf *Cloudflare) base() string {
	if cf.API != "" {
		return strings.TrimRight(cf.API, "/")
	}
	return "https://api.cloudflare.com/client/v4"
}

func (cf *Cloudflare) do(ctx context.Context, method, path, ctype string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, cf.base()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cf.Token)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	hc := cf.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r cfResponse
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("cloudflare: HTTP %d", res.StatusCode)
	}
	if !r.Success {
		msgs := []string{}
		for _, e := range r.Errors {
			msgs = append(msgs, fmt.Sprintf("%s (%d)", e.Message, e.Code))
		}
		if len(msgs) == 0 {
			msgs = append(msgs, fmt.Sprintf("HTTP %d", res.StatusCode))
		}
		return errors.New("cloudflare: " + strings.Join(msgs, "; "))
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// Verify checks the token works for the account.
func (cf *Cloudflare) Verify(ctx context.Context) error {
	if cf.Account == "" || cf.Token == "" {
		return errors.New("enter the Cloudflare account id and API token")
	}
	return cf.do(ctx, "GET", "/accounts/"+url.PathEscape(cf.Account)+"/workers/scripts", "", nil, nil)
}

// Deploy uploads the Worker with its Durable Object and secrets. migrate
// must be true the first time (it creates the Durable Object class).
func (cf *Cloudflare) Deploy(ctx context.Context, secret, portal string, migrate bool) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	meta := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": "2025-09-01",
		"bindings": []map[string]any{
			{"type": "durable_object_namespace", "name": "RELAY", "class_name": "Relay"},
			{"type": "secret_text", "name": "BOX_SECRET", "text": secret},
			{"type": "plain_text", "name": "PORTAL", "text": portal},
		},
	}
	if migrate {
		meta["migrations"] = map[string]any{"new_tag": "v1", "new_sqlite_classes": []string{"Relay"}}
	}
	mb, _ := json.Marshal(meta)
	part := func(name, ctype string, data []byte) error {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, name))
		h.Set("Content-Type", ctype)
		w, err := mw.CreatePart(h)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	w, _ := mw.CreatePart(h)
	_, _ = w.Write(mb)
	if err := part("worker.js", "application/javascript+module", Asset("worker.js")); err != nil {
		return err
	}
	for _, f := range WorkerFiles {
		// .txt + text/plain makes each a Text module (a string import).
		if err := part(f+".txt", "text/plain", Asset(f)); err != nil {
			return err
		}
	}
	mw.Close()
	return cf.do(ctx, "PUT", "/accounts/"+url.PathEscape(cf.Account)+"/workers/scripts/"+url.PathEscape(cf.Worker), mw.FormDataContentType(), &body, nil)
}

// IsMigrationError reports whether a deploy failed over the Durable Object
// migration (already applied, or missing), so the caller can retry the other way.
func IsMigrationError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "migration")
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// zoneFor finds the zone a host name belongs to (longest match).
func (cf *Cloudflare) zoneFor(ctx context.Context, host string) (*cfZone, error) {
	labels := strings.Split(host, ".")
	for i := 0; i < len(labels)-1; i++ {
		name := strings.Join(labels[i:], ".")
		var zones []cfZone
		if err := cf.do(ctx, "GET", "/zones?name="+url.QueryEscape(name), "", nil, &zones); err != nil {
			return nil, err
		}
		if len(zones) > 0 {
			return &zones[0], nil
		}
	}
	return nil, fmt.Errorf("cloudflare: no zone in this account holds %s", host)
}

type cfDomain struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
}

// Domains lists the custom domains attached to the Worker.
func (cf *Cloudflare) Domains(ctx context.Context) ([]cfDomain, error) {
	var out []cfDomain
	err := cf.do(ctx, "GET", "/accounts/"+url.PathEscape(cf.Account)+"/workers/domains?service="+url.QueryEscape(cf.Worker), "", nil, &out)
	return out, err
}

// SyncDomains makes the Worker's custom domains exactly want. A host that
// is attached to some other Worker is left alone and reported.
func (cf *Cloudflare) SyncDomains(ctx context.Context, want []string) error {
	have, err := cf.Domains(ctx)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, h := range want {
		wanted[strings.ToLower(h)] = true
	}
	var errs []string
	for _, d := range have {
		if d.Service != cf.Worker {
			continue
		}
		if !wanted[strings.ToLower(d.Hostname)] {
			if err := cf.do(ctx, "DELETE", "/accounts/"+url.PathEscape(cf.Account)+"/workers/domains/"+url.PathEscape(d.ID), "", nil, nil); err != nil {
				errs = append(errs, d.Hostname+": "+err.Error())
			}
		}
		delete(wanted, strings.ToLower(d.Hostname))
	}
	zones := map[string]*cfZone{}
	for h := range wanted {
		suffix := h[strings.Index(h, ".")+1:]
		z := zones[suffix]
		if z == nil {
			if z, err = cf.zoneFor(ctx, h); err != nil {
				errs = append(errs, h+": "+err.Error())
				continue
			}
			zones[suffix] = z
		}
		body, _ := json.Marshal(map[string]string{"hostname": h, "service": cf.Worker, "zone_id": z.ID, "environment": "production"})
		if err := cf.do(ctx, "PUT", "/accounts/"+url.PathEscape(cf.Account)+"/workers/domains", "application/json", bytes.NewReader(body), nil); err != nil {
			errs = append(errs, h+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
