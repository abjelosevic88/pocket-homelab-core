package installer

// Switching an install.sh install to another address (address.go): Caddy's file and certificate,
// install.sh's .env and Core, then every installed app's compose file. Holds the agent's work lock,
// so no install or removal runs meanwhile.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// appMarker is written next to each app's compose file at install, so the address switch knows
// which app a folder holds without asking the catalog.
const appMarker = ".pocket-app.json"

type appMark struct {
	Slug string `json:"slug"`
	Port int    `json:"port"`
}

// installedApp is one app folder in AppsDir.
type installedApp struct {
	Slug    string
	Dir     string
	Port    int
	BaseURL string // as its compose file has it now
}

var (
	reBaseURL = regexp.MustCompile(`(?m)^\s*BASE_URL:\s*["']?([^"'\s#]+)["']?\s*(?:#.*)?$`)
	reAppPort = regexp.MustCompile(`CORE_APP_URL:\s*["']?http://(?:host\.docker\.internal|127\.0\.0\.1|localhost):(\d+)`)
	reImage   = regexp.MustCompile(`(?m)^\s*image:\s*["']?ghcr\.io/[^/\s]+/([^:@"'\s]+)`)
	reSecure  = regexp.MustCompile(`(?m)^(\s*SECURE_COOKIES:\s*)(["']?)(true|false)(["']?)(\s*(?:#.*)?)$`)
)

// installedApps lists the app folders the installer made.
func (ad *Address) installedApps(ctx context.Context) ([]installedApp, error) {
	entries, err := os.ReadDir(ad.ag.cfg.AppsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var bySlug map[string]string // repo → slug, from the catalog, only when a folder has no marker
	var out []installedApp
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(ad.ag.cfg.AppsDir, e.Name())
		compose := readFile(filepath.Join(dir, "docker-compose.yml"))
		if compose == "" || !strings.Contains(compose, "CORE_APP_URL") {
			continue
		}
		app := installedApp{Dir: dir}
		if m := reBaseURL.FindStringSubmatch(compose); m != nil {
			app.BaseURL = m[1]
		}
		if m := reAppPort.FindStringSubmatch(compose); m != nil {
			app.Port, _ = strconv.Atoi(m[1])
		}
		var mark appMark
		if readJSON(filepath.Join(dir, appMarker), &mark) && mark.Slug != "" {
			app.Slug = mark.Slug
		} else if m := reImage.FindStringSubmatch(compose); m != nil {
			if bySlug == nil {
				bySlug = map[string]string{}
				if rels, err := ad.ag.src.Catalog(ctx, false); err == nil {
					for _, r := range rels {
						bySlug[r.Repo] = r.Slug
					}
				}
			}
			app.Slug = bySlug[m[1]]
		}
		if app.Slug == "" || app.Port == 0 || app.BaseURL == "" {
			ad.log.Info("address: left alone (can't tell which app it is)", "dir", dir)
			continue
		}
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (ad *Address) appURL(app installedApp, domain string) string {
	if domain != "" {
		return "https://" + app.Slug + "." + domain
	}
	return fmt.Sprintf("http://%s:%d", ad.plainHost(), app.Port)
}

// caddyfile serves Core and every app at <name>.<domain> with the certificate for *.<domain>.
func (ad *Address) caddyfile(domain string, apps []installedApp) string {
	up := ad.ag.cfg.CaddyUpstream
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by Pocket Core's installer for %s: every app at https://<app>.%s.\n", domain, domain)
	b.WriteString("# It is written again whenever an app is installed or removed or the address changes; edits here are lost.\n\n")
	fmt.Fprintf(&b, "https://%s, https://*.%s {\n\ttls /etc/caddy/tls/cert.pem /etc/caddy/tls/key.pem\n", domain, domain)
	site := func(name string, port int) {
		fmt.Fprintf(&b, "\t@%s host %s.%s\n\thandle @%s {\n\t\treverse_proxy %s:%d {\n\t\t\tflush_interval -1\n\t\t}\n\t}\n", name, name, domain, name, up, port)
	}
	site("core", ad.corePort())
	for _, a := range apps {
		if a.Slug != "core" {
			site(a.Slug, a.Port)
		}
	}
	fmt.Fprintf(&b, "\t@bare host %s\n\thandle @bare {\n\t\tredir https://core.%s{uri}\n\t}\n", domain, domain)
	b.WriteString("\thandle {\n\t\trespond \"No such app here\" 404\n\t}\n}\n")
	return b.String()
}

func (ad *Address) caddyDir(name ...string) string {
	return filepath.Join(append([]string{ad.cfg.Dir, "caddy"}, name...)...)
}

// writeCaddy puts the certificate and the Caddyfile in place; says whether anything changed.
func (ad *Address) writeCaddy(domain string, apps []installedApp) (bool, error) {
	changed := false
	put := func(path string, data []byte, mode os.FileMode) error {
		if readFile(path) == string(data) {
			return nil
		}
		changed = true
		return writeFileAtomic(path, data, mode)
	}
	if err := os.MkdirAll(ad.caddyDir("tls"), 0o700); err != nil {
		return false, err
	}
	crt, key := ad.certPaths(domain)
	if err := put(ad.caddyDir("tls", "cert.pem"), []byte(readFile(crt)), 0o600); err != nil {
		return false, err
	}
	if err := put(ad.caddyDir("tls", "key.pem"), []byte(readFile(key)), 0o600); err != nil {
		return false, err
	}
	if err := put(ad.caddyDir("Caddyfile"), []byte(ad.caddyfile(domain, apps)), 0o644); err != nil {
		return false, err
	}
	ad.ag.cfg.FilesOwner.chown(ad.caddyDir("Caddyfile"))
	return changed, nil
}

// compose runs docker compose for install.sh's own project (Core, the agent, Caddy, Tailscale).
func (ad *Address) compose(ctx context.Context, args ...string) (string, error) {
	return ad.ag.dk.run(ctx, ad.cfg.Dir, append([]string{"compose"}, args...)...)
}

// caddyUp starts the proxy (or has it read its file again).
func (ad *Address) caddyUp(ctx context.Context) error {
	if _, err := ad.compose(ctx, "--profile", "proxy", "up", "-d", "caddy"); err != nil {
		return fmt.Errorf("starting Caddy: %w", err)
	}
	c := ad.ag.cfg.CaddyContainer
	var err error
	for i := 0; i < 10; i++ { // a Caddy that just started needs a moment before it takes a reload
		if _, err = ad.ag.dk.run(ctx, "", "exec", c, "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"); err == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("Caddy didn't take the new file: %w", err)
}

// refreshCaddy is for installs and removals while there is a name: the Caddyfile lists every app.
func (ad *Address) refreshCaddy(ctx context.Context) error {
	domain := ad.domain()
	if domain == "" {
		return nil
	}
	apps, err := ad.installedApps(ctx)
	if err != nil {
		return err
	}
	if changed, err := ad.writeCaddy(domain, apps); err != nil || !changed {
		return err
	}
	return ad.caddyUp(ctx)
}

// readdress gives one app its new public address and starts it again.
func (ad *Address) readdress(ctx context.Context, app installedApp, next string, secure bool) error {
	path := filepath.Join(app.Dir, "docker-compose.yml")
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	old := readFile(path)
	s := rewriteCompose(old, app.BaseURL, next, secure)
	if s != old {
		if err := writeFileAtomic(path, []byte(s), fi.Mode().Perm()); err != nil {
			return err
		}
		ad.ag.cfg.FilesOwner.chown(path)
	}
	_ = writeJSONFile(filepath.Join(app.Dir, appMarker), appMark{app.Slug, app.Port}, 0o644)
	ad.ag.cfg.FilesOwner.chown(filepath.Join(app.Dir, appMarker))
	if _, err := ad.ag.dk.run(ctx, app.Dir, "compose", "up", "-d"); err != nil {
		return err
	}
	return nil
}

// rewriteCompose moves an app's compose file from one public address to another.
func rewriteCompose(compose, from, to string, secure bool) string {
	s := strings.ReplaceAll(compose, from, to)
	return reSecure.ReplaceAllString(s, "${1}${2}"+strconv.FormatBool(secure)+"${4}${5}")
}

func profiles(list string, add, remove string) string {
	var out []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" && p != remove && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if add != "" && !slices.Contains(out, add) {
		out = append(out, add)
	}
	return strings.Join(out, ",")
}

// waitCore waits for Core's /healthz on this machine.
func (ad *Address) waitCore(ctx context.Context) error {
	u := fmt.Sprintf("http://127.0.0.1:%d/healthz", ad.corePort())
	for i := 0; i < 60; i++ {
		if res, err := ad.ag.httpc.Get(u); err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("Pocket Core didn't come back on %s", u)
}

// switchTo moves the whole install to https://<app>.<domain>, or with domain "" back to
// http://<this machine>:<port>. Nothing happens when it is there already (beyond a renewed
// certificate or a changed app list reaching Caddy).
func (ad *Address) switchTo(ctx context.Context, domain string) error {
	ag := ad.ag
	ag.work.Lock()
	defer ag.work.Unlock()
	old := ad.domain()
	apps, err := ad.installedApps(ctx)
	if err != nil {
		return err
	}
	if domain != "" {
		changed, err := ad.writeCaddy(domain, apps)
		if err != nil {
			return err
		}
		if domain == old {
			if changed || !ad.caddyRunning(ctx) {
				return ad.caddyUp(ctx)
			}
			return nil
		}
	} else if old == "" && ad.envGet("POCKET_ADDRESS") == "" {
		return nil
	}
	ad.log.Info("address: switching", "from", firstNonEmpty(old, "this network"), "to", firstNonEmpty(domain, "this network"))

	// Core first: the apps read its sign-in (OIDC issuer) when they start.
	coreURL := ad.plainCoreURL()
	tmpl := fmt.Sprintf("http://%s:{port}", ad.plainHost())
	prof := profiles(ad.envGet("COMPOSE_PROFILES"), "", "proxy")
	if domain != "" {
		coreURL, tmpl = "https://core."+domain, "https://{slug}."+domain
		prof = profiles(ad.envGet("COMPOSE_PROFILES"), "proxy", "")
	}
	if err := ad.envSet(map[string]string{
		"BASE_URL": coreURL, "SECURE_COOKIES": strconv.FormatBool(domain != ""), "INSTALLER_PUBLIC_URL": tmpl,
		"COMPOSE_PROFILES": prof, "POCKET_ADDRESS": domain,
	}); err != nil {
		return fmt.Errorf("install.sh's .env: %w", err)
	}
	ag.cfg.PublicURL = tmpl
	if domain != "" {
		if err := writeFileAtomic(ad.state("domain"), []byte(domain+"\n"), 0o644); err != nil {
			return err
		}
		if err := ad.caddyUp(ctx); err != nil {
			return err
		}
	} else {
		_ = os.Remove(ad.state("domain"))
		_, _ = ad.compose(ctx, "--profile", "proxy", "rm", "-sf", "caddy")
	}
	if _, err := ad.compose(ctx, "up", "-d", "core"); err != nil {
		return fmt.Errorf("starting Pocket Core again: %w", err)
	}
	if err := ad.waitCore(ctx); err != nil {
		return err
	}
	for _, app := range apps {
		next := ad.appURL(app, domain)
		if next == app.BaseURL {
			continue
		}
		if err := ad.readdress(ctx, app, next, domain != ""); err != nil {
			ad.log.Warn("address: app not moved", "slug", app.Slug, "err", err)
			continue
		}
		ad.log.Info("address: app moved", "slug", app.Slug, "url", next)
	}
	return nil
}

func (ad *Address) caddyRunning(ctx context.Context) bool {
	out, err := ad.ag.dk.run(ctx, "", "inspect", "-f", "{{.State.Running}}", ad.ag.cfg.CaddyContainer)
	return err == nil && strings.TrimSpace(out) == "true"
}

// ---- the socket Core talks to ----

// Serve answers Core on the address socket until ctx ends, and keeps the address in the background.
func (ad *Address) Serve(ctx context.Context) error {
	sock := ad.cfg.Socket
	if fi, err := os.Lstat(sock); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s exists and is not a socket", sock)
		}
		os.Remove(sock)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		return err
	}
	ad.ag.cfg.FilesOwner.chown(sock) // Core runs as the person, not root
	go ad.Run(ctx)
	srv := &http.Server{Handler: ad.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Shutdown(context.Background()) }()
	ad.log.Info("address service listening", "socket", sock, "dir", ad.cfg.Dir)
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (ad *Address) reply(w http.ResponseWriter, v any, err error) {
	var p *Problem
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.As(err, &p):
		writeErr(w, http.StatusBadRequest, p.msg)
	default:
		writeErr(w, http.StatusBadGateway, err.Error())
	}
}

func decodeBody(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(v)
}

// Handler is the address service's API (and Tailscale's, tailscale.go).
func (ad *Address) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/address", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ad.view(r.Context()))
	})
	mux.HandleFunc("PUT /v1/address", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if decodeBody(r, &b) != nil {
			writeErr(w, http.StatusBadRequest, "a JSON body is required")
			return
		}
		v, err := ad.setChoice(r.Context(), b)
		ad.reply(w, v, err)
	})
	mux.HandleFunc("POST /v1/address/cloudflare/zones", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Token string `json:"token"`
		}
		if decodeBody(r, &b) != nil || strings.TrimSpace(b.Token) == "" {
			writeErr(w, http.StatusBadRequest, "token is required")
			return
		}
		zs, err := ad.cfZones(r.Context(), strings.TrimSpace(b.Token))
		if zs == nil {
			zs = []cfZone{}
		}
		ad.reply(w, map[string]any{"zones": zs}, err)
	})
	mux.HandleFunc("POST /v1/address/cloudflare/start", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ReturnURL string `json:"return_url"`
		}
		_ = decodeBody(r, &b)
		v, err := ad.startSignin(r.Context(), b.ReturnURL)
		ad.reply(w, v, err)
	})
	mux.HandleFunc("POST /v1/address/cloudflare/finish", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Code  string `json:"code"`
			State string `json:"state"`
		}
		if decodeBody(r, &b) != nil || b.Code == "" || b.State == "" {
			writeErr(w, http.StatusBadRequest, "code and state are required")
			return
		}
		v, err := ad.finishSignin(r.Context(), b.Code, b.State)
		ad.reply(w, v, err)
	})
	ad.tailscaleRoutes(mux)
	return mux
}
