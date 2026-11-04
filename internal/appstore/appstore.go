// Package appstore is the signed list of installable Pocket apps.
//
// A catalog is one JSON file: every released app with its newest version,
// the release image pinned by digest, the platforms the image is built for,
// what the app needs from the host, and the docker-compose.yml it ships.
// CI builds it from the app repositories (`pocket-core catalog build`) and
// signs it with an ed25519 key; installers trust only catalogs signed by a
// key they know. The file is an envelope around the catalog's exact bytes:
//
//	{"format":"pocket-apps/1","key_id":"…","signature":"<base64>","catalog":{…}}
//
// The signature covers the bytes of "catalog" as they appear in the file.
package appstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Format names the envelope; a reader refuses anything else.
const Format = "pocket-apps/1"

// DefaultURL is where the official catalog is published (a GitHub release
// asset of Pocket Core, rebuilt by CI; pockethomelab.com/apps.json redirects
// here).
const DefaultURL = "https://github.com/abjelosevic88/pocket-homelab-core/releases/download/catalog/apps.json"

// OfficialKeys are the keys the official catalog is signed with, as
// "<key id>:<base64 public key>".
var OfficialKeys = []string{
	"pocket-2026:33m6jL/elEVOcDfQtomIcumR/lMeWe8rzNN/npktpQ4=",
}

// Catalog is the signed content.
type Catalog struct {
	Generated time.Time `json:"generated"`
	Source    string    `json:"source"` // github.com/<owner>
	Apps      []App     `json:"apps"`
}

// App is one installable release.
type App struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category,omitempty"`
	Icon        string   `json:"icon,omitempty"` // absolute URL
	Page        string   `json:"page,omitempty"` // the app's web page
	Repo        string   `json:"repo"`
	RepoURL     string   `json:"repo_url"`
	Version     string   `json:"version"` // 0.4.5
	Tag         string   `json:"tag"`     // v0.4.5
	Released    string   `json:"released,omitempty"`
	Image       string   `json:"image"`            // ghcr.io/owner/repo
	Digest      string   `json:"digest,omitempty"` // sha256:… of the tag's manifest (list)
	Platforms   []string `json:"platforms,omitempty"`
	Container   string   `json:"container"`
	Requires    Requires `json:"requires"`
	Compose     string   `json:"compose"`
	EnvExample  string   `json:"env_example,omitempty"`
}

// Requires is what the app's compose file asks of the host, so people see
// it before they press Install.
type Requires struct {
	DockerSocket bool     `json:"docker_socket,omitempty"`
	HostNetwork  bool     `json:"host_network,omitempty"`
	Privileged   bool     `json:"privileged,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Devices      []string `json:"devices,omitempty"`
	HostFolders  []string `json:"host_folders,omitempty"`
	Services     []string `json:"services,omitempty"` // other containers in the stack
}

// Ref is the image reference Install writes: tag plus digest when known.
func (a *App) Ref() string {
	ref := a.Image + ":" + a.Version
	if a.Digest != "" {
		ref += "@" + a.Digest
	}
	return ref
}

// Supports reports whether the image runs on os/arch (true when the
// catalog does not say).
func (a *App) Supports(os, arch string) bool {
	if len(a.Platforms) == 0 {
		return true
	}
	for _, p := range a.Platforms {
		if p == os+"/"+arch || strings.HasPrefix(p, os+"/"+arch+"/") {
			return true
		}
	}
	return false
}

// Find returns the app with the slug.
func (c *Catalog) Find(slug string) *App {
	for i := range c.Apps {
		if c.Apps[i].Slug == slug {
			return &c.Apps[i]
		}
	}
	return nil
}

type envelope struct {
	Format    string          `json:"format"`
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
	Catalog   json.RawMessage `json:"catalog"`
}

// Key is a parsed "<id>:<base64>" key.
type Key struct {
	ID  string
	Pub ed25519.PublicKey
}

// ParseKeys reads "<id>:<base64 public key>" entries.
func ParseKeys(specs []string) ([]Key, error) {
	var out []Key
	for _, s := range specs {
		id, b64, ok := strings.Cut(strings.TrimSpace(s), ":")
		if !ok || id == "" {
			return nil, fmt.Errorf("catalog key %q: want <id>:<base64 public key>", s)
		}
		b, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("catalog key %s: not an ed25519 public key", id)
		}
		out = append(out, Key{ID: id, Pub: ed25519.PublicKey(b)})
	}
	return out, nil
}

// GenerateKey makes a signing key: the private half as "<id>:<base64 seed>"
// and the public half as "<id>:<base64>" for OfficialKeys or
// INSTALLER_CATALOG_KEYS.
func GenerateKey(id string) (private, public string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return id + ":" + base64.StdEncoding.EncodeToString(priv.Seed()), id + ":" + base64.StdEncoding.EncodeToString(pub), nil
}

// Sign wraps the catalog in a signed envelope. key is "<id>:<base64 seed>".
func Sign(c *Catalog, key string) ([]byte, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(key), ":")
	seed, err := base64.StdEncoding.DecodeString(b64)
	if !ok || id == "" || err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("signing key: want <id>:<base64 ed25519 seed>")
	}
	body, err := json.MarshalIndent(c, "  ", "  ")
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), body)
	head, _ := json.Marshal(struct {
		Format    string `json:"format"`
		KeyID     string `json:"key_id"`
		Signature string `json:"signature"`
	}{Format, id, base64.StdEncoding.EncodeToString(sig)})
	var b bytes.Buffer
	b.WriteString("{\n  ")
	b.Write(bytes.TrimSuffix(bytes.TrimPrefix(head, []byte("{")), []byte("}")))
	b.WriteString(",\n  \"catalog\": ")
	b.Write(body)
	b.WriteString("\n}\n")
	return b.Bytes(), nil
}

// Verify checks the envelope against the trusted keys and returns the
// catalog and the key id that signed it.
func Verify(data []byte, keys []Key) (*Catalog, string, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, "", fmt.Errorf("app catalog: %w", err)
	}
	if env.Format != Format {
		return nil, "", fmt.Errorf("app catalog: unknown format %q", env.Format)
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, "", errors.New("app catalog: unreadable signature")
	}
	for _, k := range keys {
		if k.ID != env.KeyID {
			continue
		}
		if !ed25519.Verify(k.Pub, env.Catalog, sig) {
			return nil, "", fmt.Errorf("app catalog: the signature does not match key %s; refusing it", k.ID)
		}
		var c Catalog
		if err := json.Unmarshal(env.Catalog, &c); err != nil {
			return nil, "", fmt.Errorf("app catalog: %w", err)
		}
		return &c, k.ID, nil
	}
	return nil, "", fmt.Errorf("app catalog: signed with key %q, which this installer does not trust", env.KeyID)
}

// Fetch downloads and verifies a catalog.
func Fetch(ctx context.Context, hc *http.Client, url string, keys []Key) (*Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, application/octet-stream")
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("app catalog: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("app catalog %s: %s", url, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("app catalog: %w", err)
	}
	c, _, err := Verify(b, keys)
	return c, err
}

// SameApps reports whether two catalogs list the same releases (ignoring
// when they were generated), so CI only republishes on a change.
func SameApps(a, b *Catalog) bool {
	if a == nil || b == nil {
		return false
	}
	x, _ := json.Marshal(a.Apps)
	y, _ := json.Marshal(b.Apps)
	return bytes.Equal(x, y)
}

// PublicKey returns the "<id>:<base64>" public half of a signing key.
func PublicKey(private string) (string, error) {
	id, b64, ok := strings.Cut(strings.TrimSpace(private), ":")
	seed, err := base64.StdEncoding.DecodeString(b64)
	if !ok || id == "" || err != nil || len(seed) != ed25519.SeedSize {
		return "", errors.New("signing key: want <id>:<base64 ed25519 seed>")
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return id + ":" + base64.StdEncoding.EncodeToString(pub), nil
}
