package installer

// The address of a Core installed with deploy/install.sh, as the owner chooses it (the same three
// choices as a Pocket Box, modules/address.py + names.py in pocket-homelab-box, for Docker):
//
//	pocket      Pocket's free address, <random>.box.pockethomelab.net (Pocket Names): the name points
//	            at this machine's home-network address, and Let's Encrypt's DNS check goes through
//	            Pocket Names, so the certificate's key never leaves the machine
//	cloudflare  the owner's own domain on Cloudflare: <sub>.<zone> and *.<sub>.<zone> pointed here with
//	            their token ("Sign in with Cloudflare" or a pasted API token), the certificate over DNS-01
//	plain       no name: every app at http://<this machine>:<port>, as install.sh starts out
//
// Either name gets one certificate for <name> and *.<name>. Once it is there, the agent switches the
// whole install over in one go: Caddy (the install's own container, profile "proxy") serves every app
// at https://<app>.<name> from a Caddyfile it writes itself; each installed app's compose file gets its
// new BASE_URL and is started again (it registers with Core under the new address); install.sh's .env
// gets Core's; Core is started again last. Going back to plain undoes it the same way.
//
// Core reaches this over its own unix socket (INSTALLER_ADDRESS_SOCKET, mounted into Core as
// BOX_ADDRESS) with the same API as the Pocket Box's address service, so Core's address page and
// setup step work unchanged:
//
//	GET  /v1/address                       the choice, the address, how far getting it got
//	PUT  /v1/address  {mode, zone_id?, sub?, token? | pending?}
//	POST /v1/address/cloudflare/zones  {token}           the zones a pasted API token can edit
//	POST /v1/address/cloudflare/start  {return_url?}     "Sign in with Cloudflare": the URL to open
//	POST /v1/address/cloudflare/finish {code, state}     back from Cloudflare: the zones, a pending id
//	POST /v1/address/retry                               try again now, not within 3 minutes
//
// and Tailscale (tailscale.go). State lives in <install dir>/address; secrets in its private/ (0700).

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/httpreq"
	"github.com/go-acme/lego/v4/registration"
)

const (
	modePocket     = "pocket"
	modeCloudflare = "cloudflare"
	modePlain      = "plain"
	recordComment  = "Pocket Box" // on the DNS records this makes in the owner's Cloudflare zone
	cfAPI          = "https://api.cloudflare.com/client/v4"
)

var addressModes = []string{modePocket, modeCloudflare, modePlain}

// Cloudflare's "Create token" page with Zone · Read and DNS · Edit filled in.
var cfTokenLink = "https://dash.cloudflare.com/profile/api-tokens?" + url.Values{
	"permissionGroupKeys": {`[{"key":"zone","type":"read"},{"key":"dns","type":"edit"}]`},
	"accountId":           {"*"}, "zoneId": {"all"}, "name": {"Pocket"},
}.Encode()

var subRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// AddressConfig: where install.sh put things, and whom to ask.
type AddressConfig struct {
	Socket    string // INSTALLER_ADDRESS_SOCKET; empty = no address choice (e.g. install.sh --domain)
	Dir       string // INSTALLER_POCKET_DIR: install.sh's folder (.env, docker-compose.yml, caddy/)
	NamesURL  string // INSTALLER_NAMES_URL: Pocket Names
	ACME      string // INSTALLER_ACME_CA: Let's Encrypt's directory (staging for tests)
	Tailscale string // INSTALLER_TAILSCALE_CONTAINER (pocket-tailscale)
}

// Problem is something the owner can fix; its text is shown to them.
type Problem struct{ msg string }

func (p *Problem) Error() string { return p.msg }

func problemf(format string, a ...any) error { return &Problem{fmt.Sprintf(format, a...)} }

// Address is the address service.
type Address struct {
	ag    *Agent
	cfg   AddressConfig
	log   *slog.Logger
	httpc *http.Client
	kick  chan struct{}

	mu      sync.Mutex
	starts  map[string]cfStart  // "Sign in with Cloudflare" under way, by state
	signins map[string]cfSignin // finished sign-ins waiting for the domain choice, by pending id
	oauth   *cfOAuth
	oauthAt time.Time
}

func newAddress(ag *Agent, cfg AddressConfig, log *slog.Logger) *Address {
	return &Address{ag: ag, cfg: cfg, log: log, httpc: &http.Client{Timeout: 30 * time.Second}, kick: make(chan struct{}, 1),
		starts: map[string]cfStart{}, signins: map[string]cfSignin{}}
}

func (ad *Address) state(name ...string) string {
	return filepath.Join(append([]string{ad.cfg.Dir, "address"}, name...)...)
}
func (ad *Address) private(name ...string) string {
	return filepath.Join(append([]string{ad.cfg.Dir, "address", "private"}, name...)...)
}

// ---- small file helpers ----

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(b, v) == nil
}

func writeJSONFile(path string, v any, mode os.FileMode) error {
	b, _ := json.Marshal(v)
	return writeFileAtomic(path, b, mode)
}

// ---- install.sh's .env ----

func (ad *Address) envPath() string { return filepath.Join(ad.cfg.Dir, ".env") }

func (ad *Address) envGet(key string) string {
	v := ""
	for _, line := range strings.Split(readFile(ad.envPath()), "\n") {
		if k, val, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			v = strings.TrimSpace(val)
		}
	}
	return v
}

// envSet changes (or adds) keys in .env and keeps everything else as it is.
func (ad *Address) envSet(kv map[string]string) error {
	path := ad.envPath()
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(readFile(path), "\n"), "\n")
	done := map[string]bool{}
	for i, line := range lines {
		if k, _, ok := strings.Cut(line, "="); ok {
			k = strings.TrimSpace(k)
			if v, want := kv[k]; want {
				lines[i] = k + "=" + v
				done[k] = true
			}
		}
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+"="+kv[k])
	}
	if err := writeFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), fi.Mode().Perm()); err != nil {
		return err
	}
	ad.ag.cfg.FilesOwner.chown(path)
	return nil
}

// ---- the choice and how far it got ----

type addrChoice struct {
	Mode string `json:"mode"`
	At   int64  `json:"at"`
}

func (ad *Address) choice() string {
	var c addrChoice
	if readJSON(ad.state("choice.json"), &c) && slices.Contains(addressModes, c.Mode) {
		return c.Mode
	}
	return ""
}

func (ad *Address) domain() string { return strings.TrimSpace(readFile(ad.state("domain"))) }

type addrStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
	At      int64  `json:"at"`
}

// status says how far getting the name got (asking | certificate | waiting | limited | offline | failed
// | signin | ready | plain); only until there is a name, and afterwards for ready and plain.
func (ad *Address) status(state, msg string) {
	if state != "ready" && state != modePlain && ad.domain() != "" {
		return
	}
	_ = writeJSONFile(ad.state("status.json"), addrStatus{state, msg, time.Now().Unix()}, 0o644)
}

// problem: the last thing that went wrong, even once there is a name (a renewal, a lapsed
// Cloudflare sign-in); cleared by the next run that works.
func (ad *Address) problem(state, msg string) {
	if state == "" {
		_ = os.Remove(ad.state("problem.json"))
		return
	}
	_ = writeJSONFile(ad.state("problem.json"), addrStatus{state, msg, time.Now().Unix()}, 0o644)
}

type cfView struct {
	Zone   string `json:"zone"`
	Sub    string `json:"sub"`
	Domain string `json:"domain"`
	Kind   string `json:"kind"`
}

func (ad *Address) view(ctx context.Context) map[string]any {
	mode := ad.choice()
	var st addrStatus
	readJSON(ad.state("status.json"), &st)
	var prob *addrStatus
	if p := (addrStatus{}); readJSON(ad.state("problem.json"), &p) {
		prob = &p
	}
	var cfv *cfView
	var c cfCred
	if mode == modeCloudflare && readJSON(ad.private("cloudflare.json"), &c) && c.Zone != "" {
		cfv = &cfView{c.Zone, c.Sub, c.Sub + "." + c.Zone, c.Kind}
	}
	var modeOut any
	if mode != "" {
		modeOut = mode
	}
	return map[string]any{
		"mode": modeOut, "chosen": mode != "", "can_choose": true, "domain": ad.domain(),
		"status": st, "problem": prob, "cloudflare": cfv,
		"options":   map[string]any{"oauth": ad.oauthConfig(ctx) != nil, "token_link": cfTokenLink},
		"modes":     addressModes,
		"plain_url": ad.plainCoreURL(),
	}
}

func (ad *Address) plainHost() string {
	if h := ad.envGet("POCKET_HOST"); h != "" {
		return h
	}
	if ip := homeAddress(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

func (ad *Address) corePort() int {
	if p, err := strconv.Atoi(ad.envGet("CORE_PORT")); err == nil && p > 0 {
		return p
	}
	return 8179
}

func (ad *Address) plainCoreURL() string {
	return fmt.Sprintf("http://%s:%d", ad.plainHost(), ad.corePort())
}

// noTarget: what to do when the name has nothing to point at.
const noTarget = "This machine has no home-network address (10.x, 172.16–31.x or 192.168.x), as on a VPS. Connect Tailscale (Away from home), and the name points at this machine's Tailscale address instead: a padlock on every device on your tailnet, and nothing open to the internet."

// nameTarget is the address the name points at: this machine's home-network one, so phones at home
// reach it without anything installed; or its Tailscale one when it has none (a VPS) or the owner
// chose the tailnet (POCKET_NAME_ON=tailscale in .env, install.sh --name-on tailscale).
func (ad *Address) nameTarget() string {
	if ad.envGet("POCKET_NAME_ON") != "tailscale" {
		if ip := homeAddress(); ip != "" {
			return ip
		}
	}
	return tailscaleIPv4()
}

// tailscaleIPv4: this machine's address on the tailnet (Pocket's Tailscale container or one of
// its own: either way the tailscale0 interface, the agent runs on the host network).
func tailscaleIPv4() string {
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	for _, s := range hostTailscale() {
		if ip, err := netip.ParseAddr(s); err == nil && ip.Is4() && cgnat.Contains(ip) {
			return ip.String()
		}
	}
	return ""
}

// homeAddress is the IPv4 address this machine reaches the internet from, if it is a home-network
// one (Pocket Names points names at those, or at a tailnet's).
func homeAddress() string {
	c, err := net.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	ip, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return ""
	}
	ip = ip.Unmap()
	if !ip.Is4() || !ip.IsPrivate() {
		return ""
	}
	return ip.String()
}

// setChoice records the owner's choice and starts on it.
func (ad *Address) setChoice(ctx context.Context, b map[string]any) (map[string]any, error) {
	mode, _ := b["mode"].(string)
	if !slices.Contains(addressModes, mode) {
		return nil, problemf("mode must be pocket, cloudflare or plain")
	}
	if mode == modeCloudflare {
		sub := strings.ToLower(strings.TrimSpace(jstr(b["sub"])))
		if sub == "" {
			sub = "pocket"
		}
		if !subRe.MatchString(sub) {
			return nil, problemf("The name can have letters, digits and dashes (up to 40), like “pocket” or “home”.")
		}
		var cred cfCred
		switch {
		case jstr(b["pending"]) != "":
			ad.mu.Lock()
			p, ok := ad.signins[jstr(b["pending"])]
			delete(ad.signins, jstr(b["pending"]))
			ad.mu.Unlock()
			if !ok || time.Since(p.at) > 20*time.Minute {
				return nil, problemf("This Cloudflare sign-in has expired. Sign in again.")
			}
			cred = p.cred
		case strings.TrimSpace(jstr(b["token"])) != "":
			cred = cfCred{Kind: "token", Token: strings.TrimSpace(jstr(b["token"]))}
		default:
			return nil, problemf("Sign in with Cloudflare or paste an API token.")
		}
		token := cred.bearer()
		zs, err := ad.cfZones(ctx, token)
		if err != nil {
			return nil, err
		}
		var zone *cfZone
		for i := range zs {
			if zs[i].ID == jstr(b["zone_id"]) {
				zone = &zs[i]
			}
		}
		if zone == nil {
			return nil, problemf("That domain isn't one this Cloudflare sign-in can change.")
		}
		domain := sub + "." + zone.Name
		if n, err := ad.cfRecordsInTheWay(ctx, token, zone.ID, domain); err != nil {
			return nil, err
		} else if n > 0 {
			return nil, problemf("%s is already used in your Cloudflare DNS. Pick another name, or remove those records first.", domain)
		}
		cred.ZoneID, cred.Zone, cred.Sub = zone.ID, zone.Name, sub
		if err := os.MkdirAll(ad.private(), 0o700); err != nil {
			return nil, err
		}
		if old := readFile(ad.private("cloudflare.json")); old != "" {
			_ = writeFileAtomic(ad.private("cloudflare-old.json"), []byte(old), 0o600)
		}
		if err := writeJSONFile(ad.private("cloudflare.json"), cred, 0o600); err != nil {
			return nil, err
		}
		ad.log.Info("address: cloudflare", "domain", domain, "kind", cred.Kind)
	} else {
		ad.log.Info("address", "mode", mode)
	}
	if err := writeJSONFile(ad.state("choice.json"), addrChoice{mode, time.Now().Unix()}, 0o644); err != nil {
		return nil, err
	}
	if mode != modePlain {
		_ = writeJSONFile(ad.state("status.json"), addrStatus{"asking", "", time.Now().Unix()}, 0o644)
	}
	ad.Kick()
	return ad.view(ctx), nil
}

func jstr(v any) string { s, _ := v.(string); return s }

// Kick makes the worker look at the choice now.
func (ad *Address) Kick() {
	select {
	case ad.kick <- struct{}{}:
	default:
	}
}

// ---- the worker ----

// Run keeps the address as chosen: right away, again every 3 minutes until a chosen name works, then
// every 30 (the home address, a sign of life for Pocket Names, the certificate 30 days before it ends).
func (ad *Address) Run(ctx context.Context) {
	_ = os.MkdirAll(ad.private(), 0o700)
	_ = os.Chmod(ad.private(), 0o700)
	// The template new installs use follows the address in use, whatever .env said at start.
	if d := ad.domain(); d != "" {
		ad.ag.setPublicURL("https://{slug}." + d)
	}
	t := time.NewTimer(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ad.kick:
		case <-t.C:
		}
		ad.once(ctx)
		wait := 30 * time.Minute
		if m := ad.choice(); (m == modePocket || m == modeCloudflare) && ad.domain() != ad.wanted() {
			wait = 3 * time.Minute
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(wait)
	}
}

// wanted is the name the choice is after ("" for plain, or a Pocket name not known yet).
func (ad *Address) wanted() string {
	switch ad.choice() {
	case modePocket:
		var a pocketAccount
		if readJSON(ad.private("account.json"), &a) {
			return a.Domain
		}
		return "?"
	case modeCloudflare:
		var c cfCred
		if readJSON(ad.private("cloudflare.json"), &c) {
			return c.Sub + "." + c.Zone
		}
	}
	return ""
}

func (ad *Address) once(ctx context.Context) {
	mode := ad.choice()
	if mode == "" {
		return
	}
	var err error
	switch mode {
	case modePocket:
		err = ad.runPocket(ctx)
	case modeCloudflare:
		err = ad.runCloudflare(ctx)
	case modePlain:
		err = ad.runPlain(ctx)
	}
	if err == nil {
		ad.problem("", "")
		return
	}
	ad.log.Warn("address: not this time", "mode", mode, "err", err)
	var lim *limited
	var wait *waiting
	var sign *signIn
	var urlErr *url.Error
	var owner *Problem
	switch {
	case errors.As(err, &owner): // something the owner can do (connect Tailscale, …): say what
		ad.status("failed", owner.msg)
		ad.problem("failed", owner.msg)
	case errors.As(err, &sign):
		msg := "The Cloudflare sign-in stopped working. Sign in again in Pocket Core → Settings."
		ad.status("signin", msg)
		ad.problem("signin", msg)
	case errors.As(err, &lim):
		ad.status("limited", lim.msg)
		ad.problem("limited", lim.msg)
	case errors.As(err, &wait):
		ad.status("waiting", "Pocket Names hasn't let this machine in yet; it asks again every few minutes.")
	case errors.As(err, &urlErr):
		ad.status("offline", "This machine can't reach the internet right now.")
		ad.problem("failed", err.Error())
	default:
		ad.status("failed", "Getting the name didn't work this time; it is tried again in a few minutes.")
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		ad.problem("failed", msg)
	}
}

// ---- Pocket's free address (Pocket Names) ----

type pocketAccount struct {
	Name   string `json:"name"`
	Token  string `json:"token"`
	Domain string `json:"domain"`
}

type limited struct{ msg string }
type waiting struct{ msg string }
type rejected struct{ msg string }

func (e *limited) Error() string  { return e.msg }
func (e *waiting) Error() string  { return e.msg }
func (e *rejected) Error() string { return e.msg }

// What each of Pocket Names' limits means for the owner, by the start of its error text.
var namesLimits = [][2]string{
	{"too many new names from this address", "Pocket Names gives each home internet address 3 new names a day, and this address has used them today. It is asked again by itself and the name comes as soon as one frees up, within a day at most."},
	{"too many new names today", "Pocket Names has given out all of today's new names. It is asked again by itself; the name comes tomorrow at the latest."},
	{"too many new boxes this week", "Pocket Names has reached this week's limit for new certificates. It is asked again by itself; the padlock comes in a few days."},
	{"too many certificate attempts", "This machine asked for its certificate too often this week. It asks again by itself in a few days."},
}

func (ad *Address) names(ctx context.Context, method, path string, body any, acct *pocketAccount) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ad.cfg.NamesURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pocket-core-installer")
	if acct != nil {
		req.SetBasicAuth(acct.Name, acct.Token)
	}
	res, err := ad.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	text := strings.TrimSpace(jstr(out["error"]))
	if text == "" {
		text = strings.TrimSpace(string(raw))
	}
	switch {
	case res.StatusCode == 401:
		return nil, &rejected{text}
	case res.StatusCode == 429:
		for _, l := range namesLimits {
			if strings.HasPrefix(text, l[0]) {
				return nil, &limited{l[1]}
			}
		}
		return nil, &limited{"Pocket Names is holding new names back for now (" + text + "). It is asked again by itself."}
	case res.StatusCode == 403 || res.StatusCode == 503:
		return nil, &waiting{fmt.Sprintf("Pocket Names %s %s: HTTP %d %s", method, path, res.StatusCode, text)}
	case res.StatusCode >= 300:
		return nil, fmt.Errorf("Pocket Names %s %s: HTTP %d %s", method, path, res.StatusCode, text)
	}
	return out, nil
}

func (ad *Address) pocketAccount(ctx context.Context) (*pocketAccount, error) {
	var a pocketAccount
	if readJSON(ad.private("account.json"), &a) && a.Name != "" {
		return &a, nil
	}
	out, err := ad.names(ctx, http.MethodPost, "/v1/register", nil, nil)
	if err != nil {
		return nil, err
	}
	a = pocketAccount{Name: jstr(out["name"]), Token: jstr(out["token"]), Domain: jstr(out["domain"])}
	if a.Name == "" || a.Token == "" || a.Domain == "" {
		return nil, fmt.Errorf("Pocket Names answered without a name")
	}
	if err := writeJSONFile(ad.private("account.json"), a, 0o600); err != nil {
		return nil, err
	}
	ad.log.Info("address: registered", "domain", a.Domain)
	return &a, nil
}

type sentAddress struct {
	IP     string `json:"ip"`
	Domain string `json:"domain"`
	At     int64  `json:"at"`
}

// sendAddress points the Pocket name at this machine's home address, when it changed and twice a
// day as a sign of life (names that go quiet for months are given back).
func (ad *Address) sendAddress(ctx context.Context, a *pocketAccount) error {
	ip := ad.nameTarget()
	if ip == "" {
		if ad.domain() == a.Domain {
			return nil // keeps the last one
		}
		return problemf("%s", noTarget)
	}
	var last sentAddress
	if readJSON(ad.private("sent.json"), &last) && last.IP == ip && last.Domain == a.Domain && time.Since(time.Unix(last.At, 0)) < 12*time.Hour {
		return nil
	}
	if _, err := ad.names(ctx, http.MethodPut, "/v1/address", map[string]string{"ip": ip}, a); err != nil {
		return err
	}
	ad.log.Info("address: pointed", "domain", a.Domain, "ip", ip)
	return writeJSONFile(ad.private("sent.json"), sentAddress{ip, a.Domain, time.Now().Unix()}, 0o600)
}

func (ad *Address) runPocket(ctx context.Context) error {
	if ad.domain() == "" {
		ad.status("asking", "")
	}
	a, err := ad.pocketAccount(ctx)
	if err != nil {
		return err
	}
	if err = ad.sendAddress(ctx, a); err != nil {
		var rej *rejected
		if !errors.As(err, &rej) {
			return err
		}
		// The name was given back (unused for months, or removed by hand): start over with a new one.
		ad.log.Info("address: Pocket Names no longer knows the name; registering again", "domain", a.Domain)
		_ = os.Remove(ad.private("account.json"))
		_ = os.Remove(ad.private("sent.json"))
		if a, err = ad.pocketAccount(ctx); err != nil {
			return err
		}
		if err = ad.sendAddress(ctx, a); err != nil {
			return err
		}
	}
	if !ad.certOK(a.Domain, 30) {
		ad.status("certificate", "")
		hc := httpreq.NewDefaultConfig()
		hc.Endpoint, _ = url.Parse(strings.TrimRight(ad.cfg.NamesURL, "/") + "/acme")
		hc.Username, hc.Password = a.Name, a.Token
		hc.PropagationTimeout, hc.PollingInterval = 5*time.Minute, 5*time.Second
		p, err := httpreq.NewDNSProviderConfig(hc)
		if err != nil {
			return err
		}
		if err := ad.obtain(a.Domain, p); err != nil {
			return err
		}
	}
	if !ad.certOK(a.Domain, 0) {
		return fmt.Errorf("no certificate for %s yet", a.Domain)
	}
	if err := ad.switchTo(ctx, a.Domain); err != nil {
		return err
	}
	ad.status("ready", "")
	ad.cfForget(ctx, "cloudflare.json", "")
	ad.cfForget(ctx, "cloudflare-old.json", "")
	return nil
}

// pocketForget gives Pocket's free address back once it is no longer used (best effort).
func (ad *Address) pocketForget(ctx context.Context) {
	var a pocketAccount
	if !readJSON(ad.private("account.json"), &a) {
		return
	}
	if _, err := ad.names(ctx, http.MethodDelete, "/v1/box", nil, &a); err != nil {
		var rej *rejected
		if !errors.As(err, &rej) {
			ad.log.Info("address: couldn't give the Pocket name back now", "err", err)
			return
		}
	}
	ad.log.Info("address: gave the Pocket name back", "domain", a.Domain)
	_ = os.Remove(ad.private("account.json"))
	_ = os.Remove(ad.private("sent.json"))
}

// ---- the owner's own domain on Cloudflare ----

type cfCred struct {
	Kind         string `json:"kind"` // token | oauth
	Token        string `json:"token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	TokenURL     string `json:"token_url,omitempty"`
	ZoneID       string `json:"zone_id,omitempty"`
	Zone         string `json:"zone,omitempty"`
	Sub          string `json:"sub,omitempty"`
}

func (c *cfCred) bearer() string {
	if c.Kind == "oauth" {
		return c.AccessToken
	}
	return c.Token
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type signIn struct{ msg string }

func (e *signIn) Error() string { return e.msg }

func (ad *Address) cf(ctx context.Context, method, path, token string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfAPI+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "pocket-core-installer")
	res, err := ad.httpc.Do(req)
	if err != nil {
		return nil, problemf("This machine can't reach Cloudflare right now (%v).", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out)
	codes := map[float64]bool{}
	var msgs []string
	if errs, ok := out["errors"].([]any); ok {
		for _, e := range errs {
			if m, ok := e.(map[string]any); ok {
				c, _ := m["code"].(float64)
				codes[c] = true
				msgs = append(msgs, jstr(m["message"]))
			}
		}
	}
	// 6003/6111/6100: not a token at all (Cloudflare answers 400); 9106/10000/10001: refused.
	if res.StatusCode == 401 || res.StatusCode == 403 || codes[6003] || codes[6100] || codes[6111] || codes[9106] || codes[10000] || codes[10001] {
		return nil, &signIn{"Cloudflare didn't accept this sign-in. Check that the token can read zones and edit DNS."}
	}
	if ok, _ := out["success"].(bool); res.StatusCode >= 400 || !ok {
		m := strings.Join(msgs, "; ")
		if m == "" {
			m = res.Status
		}
		return nil, problemf("Cloudflare said: %s", m)
	}
	return out, nil
}

func (ad *Address) cfZones(ctx context.Context, token string) ([]cfZone, error) {
	out, err := ad.cf(ctx, http.MethodGet, "/zones?per_page=50&status=active", token, nil)
	if err != nil {
		var s *signIn
		if errors.As(err, &s) {
			return nil, problemf("%s", s.msg)
		}
		return nil, err
	}
	var zs []cfZone
	for _, z := range asList(out["result"]) {
		zs = append(zs, cfZone{ID: jstr(z["id"]), Name: jstr(z["name"])})
	}
	return zs, nil
}

func asList(v any) []map[string]any {
	var out []map[string]any
	if l, ok := v.([]any); ok {
		for _, e := range l {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// cfRecordsInTheWay counts records at <domain> or *.<domain> this didn't make: never taken over.
func (ad *Address) cfRecordsInTheWay(ctx context.Context, token, zoneID, domain string) (int, error) {
	n := 0
	for _, name := range []string{domain, "*." + domain} {
		out, err := ad.cf(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?per_page=50&name="+url.QueryEscape(name), token, nil)
		if err != nil {
			return 0, err
		}
		for _, r := range asList(out["result"]) {
			if jstr(r["comment"]) != recordComment {
				n++
			}
		}
	}
	return n, nil
}

// cfToken: a working token; a Cloudflare sign-in is refreshed when it runs out.
func (ad *Address) cfToken(ctx context.Context, c *cfCred) (string, error) {
	if c.Kind != "oauth" {
		return c.Token, nil
	}
	if time.Now().Unix() < c.ExpiresAt-600 {
		return c.AccessToken, nil
	}
	if c.RefreshToken == "" {
		return "", &signIn{"the Cloudflare sign-in ran out"}
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {c.RefreshToken}, "client_id": {c.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := ad.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	if res.StatusCode == 400 || res.StatusCode == 401 {
		return "", &signIn{"Cloudflare no longer accepts the sign-in"}
	}
	if res.StatusCode != 200 || out.AccessToken == "" {
		return "", fmt.Errorf("Cloudflare token refresh: %s", res.Status)
	}
	c.AccessToken = out.AccessToken
	if out.RefreshToken != "" {
		c.RefreshToken = out.RefreshToken
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	c.ExpiresAt = time.Now().Unix() + out.ExpiresIn
	if err := writeJSONFile(ad.private("cloudflare.json"), c, 0o600); err != nil {
		return "", err
	}
	return c.AccessToken, nil
}

// cfPoint keeps <sub>.<zone> and *.<sub>.<zone> on the home address: A records only this looks after.
func (ad *Address) cfPoint(ctx context.Context, c *cfCred, token, ip string) error {
	domain := c.Sub + "." + c.Zone
	var last sentAddress
	if readJSON(ad.private("cf-sent.json"), &last) && last.IP == ip && last.Domain == domain && time.Since(time.Unix(last.At, 0)) < 12*time.Hour {
		return nil
	}
	for _, name := range []string{domain, "*." + domain} {
		out, err := ad.cf(ctx, http.MethodGet, "/zones/"+c.ZoneID+"/dns_records?type=A&name="+url.QueryEscape(name), token, nil)
		if err != nil {
			return err
		}
		rec := map[string]any{"type": "A", "name": name, "content": ip, "ttl": 300, "proxied": false, "comment": recordComment}
		var mine map[string]any
		for _, r := range asList(out["result"]) {
			if jstr(r["comment"]) == recordComment {
				mine = r
				break
			}
		}
		switch {
		case mine == nil:
			_, err = ad.cf(ctx, http.MethodPost, "/zones/"+c.ZoneID+"/dns_records", token, rec)
		case jstr(mine["content"]) != ip:
			_, err = ad.cf(ctx, http.MethodPut, "/zones/"+c.ZoneID+"/dns_records/"+jstr(mine["id"]), token, rec)
		}
		if err != nil {
			return err
		}
	}
	ad.log.Info("address: pointed", "domain", domain, "ip", ip, "via", "cloudflare")
	return writeJSONFile(ad.private("cf-sent.json"), sentAddress{ip, domain, time.Now().Unix()}, 0o600)
}

// cfForget removes the records made under an earlier Cloudflare name (best effort), and that name;
// not those of keep, the name in use (signing in again keeps the same name).
func (ad *Address) cfForget(ctx context.Context, file, keep string) {
	var c cfCred
	if !readJSON(ad.private(file), &c) {
		return
	}
	if c.ZoneID != "" && c.Sub+"."+c.Zone != keep {
		token := c.bearer()
		if file == "cloudflare.json" {
			if t, err := ad.cfToken(ctx, &c); err == nil {
				token = t
			}
		}
		domain := c.Sub + "." + c.Zone
		for _, name := range []string{domain, "*." + domain} {
			out, err := ad.cf(ctx, http.MethodGet, "/zones/"+c.ZoneID+"/dns_records?name="+url.QueryEscape(name), token, nil)
			if err != nil {
				ad.log.Info("address: couldn't remove the old Cloudflare records", "err", err)
				break
			}
			for _, r := range asList(out["result"]) {
				if jstr(r["comment"]) == recordComment {
					_, _ = ad.cf(ctx, http.MethodDelete, "/zones/"+c.ZoneID+"/dns_records/"+jstr(r["id"]), token, nil)
				}
			}
		}
		ad.log.Info("address: removed from Cloudflare", "domain", domain)
	}
	_ = os.Remove(ad.private(file))
	if file == "cloudflare.json" {
		_ = os.Remove(ad.private("cf-sent.json"))
	}
}

func (ad *Address) runCloudflare(ctx context.Context) error {
	var c cfCred
	if !readJSON(ad.private("cloudflare.json"), &c) || c.ZoneID == "" {
		return problemf("The Cloudflare sign-in is missing. Choose your domain again.")
	}
	domain := c.Sub + "." + c.Zone
	if ad.domain() != domain {
		ad.status("asking", "")
	}
	token, err := ad.cfToken(ctx, &c)
	if err != nil {
		return err
	}
	if ip := ad.nameTarget(); ip != "" {
		if err := ad.cfPoint(ctx, &c, token, ip); err != nil {
			return err
		}
	} else if ad.domain() != domain {
		return problemf("%s", noTarget)
	}
	if !ad.certOK(domain, 30) {
		ad.status("certificate", "")
		cc := cloudflare.NewDefaultConfig()
		cc.AuthToken, cc.ZoneToken = token, token
		cc.PropagationTimeout, cc.PollingInterval = 5*time.Minute, 5*time.Second
		p, err := cloudflare.NewDNSProviderConfig(cc)
		if err != nil {
			return err
		}
		if err := ad.obtain(domain, p); err != nil {
			return err
		}
	}
	if !ad.certOK(domain, 0) {
		return fmt.Errorf("no certificate for %s yet", domain)
	}
	if err := ad.switchTo(ctx, domain); err != nil {
		return err
	}
	ad.status("ready", "")
	ad.pocketForget(ctx)
	ad.cfForget(ctx, "cloudflare-old.json", domain)
	return nil
}

func (ad *Address) runPlain(ctx context.Context) error {
	if err := ad.switchTo(ctx, ""); err != nil {
		return err
	}
	ad.status(modePlain, "")
	ad.pocketForget(ctx)
	ad.cfForget(ctx, "cloudflare.json", "")
	ad.cfForget(ctx, "cloudflare-old.json", "")
	return nil
}

// ---- "Sign in with Cloudflare" (OAuth with PKCE as a public client; Pocket Names publishes the
// client id and sends the browser back here; the verifier never leaves this agent) ----

type cfOAuth struct {
	ClientID    string `json:"client_id"`
	RedirectURI string `json:"redirect_uri"`
	Scopes      string `json:"scopes"`
	Authorize   string `json:"authorize"`
	Token       string `json:"token"`
}

type cfStart struct {
	verifier string
	oauth    cfOAuth
	at       time.Time
}

type cfSignin struct {
	cred cfCred
	at   time.Time
}

func (ad *Address) oauthConfig(ctx context.Context) *cfOAuth {
	ad.mu.Lock()
	if time.Since(ad.oauthAt) < 5*time.Minute {
		o := ad.oauth
		ad.mu.Unlock()
		return o
	}
	ad.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var o *cfOAuth
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ad.cfg.NamesURL, "/")+"/v1/cloudflare", nil); err == nil {
		if res, err := ad.httpc.Do(req); err == nil {
			var out struct {
				OAuth *cfOAuth `json:"oauth"`
			}
			if res.StatusCode == 200 && json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out) == nil && out.OAuth != nil && out.OAuth.ClientID != "" {
				o = out.OAuth
			}
			res.Body.Close()
		}
	}
	ad.mu.Lock()
	ad.oauth, ad.oauthAt = o, time.Now()
	ad.mu.Unlock()
	return o
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (ad *Address) sweep() {
	for k, v := range ad.starts {
		if time.Since(v.at) > 20*time.Minute {
			delete(ad.starts, k)
		}
	}
	for k, v := range ad.signins {
		if time.Since(v.at) > 20*time.Minute {
			delete(ad.signins, k)
		}
	}
}

func (ad *Address) startSignin(ctx context.Context, returnURL string) (map[string]any, error) {
	o := ad.oauthConfig(ctx)
	if o == nil {
		return nil, problemf("Signing in with Cloudflare isn't available yet. Use an API token instead.")
	}
	vb := make([]byte, 48)
	nb := make([]byte, 12)
	_, _ = rand.Read(vb)
	_, _ = rand.Read(nb)
	verifier := b64url(vb)
	sj, _ := json.Marshal(map[string]string{"r": returnURL, "n": fmt.Sprintf("%x", nb)})
	state := b64url(sj)
	ad.mu.Lock()
	ad.sweep()
	if len(ad.starts) > 50 {
		ad.mu.Unlock()
		return nil, problemf("Too many sign-ins started; try again in a few minutes.")
	}
	ad.starts[state] = cfStart{verifier, *o, time.Now()}
	ad.mu.Unlock()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {o.ClientID}, "redirect_uri": {o.RedirectURI}, "scope": {o.Scopes},
		"state": {state}, "code_challenge": {b64url(sum[:])}, "code_challenge_method": {"S256"}}
	return map[string]any{"url": o.Authorize + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")}, nil
}

func (ad *Address) finishSignin(ctx context.Context, code, state string) (map[string]any, error) {
	ad.mu.Lock()
	ad.sweep()
	s, ok := ad.starts[state]
	delete(ad.starts, state)
	ad.mu.Unlock()
	if !ok {
		return nil, problemf("This sign-in has expired or was already used. Start again.")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.oauth.RedirectURI},
		"client_id": {s.oauth.ClientID}, "code_verifier": {s.verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.oauth.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := ad.httpc.Do(req)
	if err != nil {
		return nil, problemf("This machine can't reach Cloudflare right now (%v).", err)
	}
	defer res.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out)
	if res.StatusCode != 200 || out.AccessToken == "" {
		why := firstNonEmpty(out.ErrorDesc, out.Error, res.Status)
		return nil, problemf("Cloudflare didn't finish the sign-in (%s). Start again.", why)
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 3600
	}
	cred := cfCred{Kind: "oauth", AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, ExpiresAt: time.Now().Unix() + out.ExpiresIn,
		ClientID: s.oauth.ClientID, TokenURL: s.oauth.Token}
	zs, err := ad.cfZones(ctx, cred.AccessToken)
	if err != nil {
		return nil, err
	}
	pb := make([]byte, 18)
	_, _ = rand.Read(pb)
	pid := b64url(pb)
	ad.mu.Lock()
	ad.signins[pid] = cfSignin{cred, time.Now()}
	ad.mu.Unlock()
	return map[string]any{"pending": pid, "zones": zs}, nil
}

// ---- Let's Encrypt ----

type acmeUser struct {
	key crypto.PrivateKey
	reg *registration.Resource
}

func (u *acmeUser) GetEmail() string                        { return "" }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func (ad *Address) acmeUser() (*acmeUser, error) {
	dir := ad.private("acme")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	u := &acmeUser{}
	if b, err := os.ReadFile(filepath.Join(dir, "account.key")); err == nil {
		if blk, _ := pem.Decode(b); blk != nil {
			if k, err := x509.ParseECPrivateKey(blk.Bytes); err == nil {
				u.key = k
			}
		}
	}
	if u.key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, _ := x509.MarshalECPrivateKey(k)
		if err := writeFileAtomic(filepath.Join(dir, "account.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			return nil, err
		}
		u.key = k
		_ = os.Remove(filepath.Join(dir, "registration.json"))
	}
	var reg registration.Resource
	if readJSON(filepath.Join(dir, "registration.json"), &reg) && reg.URI != "" && readFile(filepath.Join(dir, "ca")) == ad.cfg.ACME {
		u.reg = &reg
	}
	return u, nil
}

func (ad *Address) certPaths(domain string) (string, string) {
	base := ad.private("certs", domain)
	return base + ".crt", base + ".key"
}

// certOK: a certificate for domain and *.domain that lasts at least another `days` days.
func (ad *Address) certOK(domain string, days int) bool {
	crt, key := ad.certPaths(domain)
	if _, err := os.Stat(key); err != nil {
		return false
	}
	blk, _ := pem.Decode([]byte(readFile(crt)))
	if blk == nil {
		return false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil || time.Now().Add(time.Duration(days)*24*time.Hour).After(c.NotAfter) {
		return false
	}
	return slices.Contains(c.DNSNames, domain) && slices.Contains(c.DNSNames, "*."+domain)
}

// obtain gets a certificate for domain and *.domain over DNS-01 through p.
func (ad *Address) obtain(domain string, p challenge.Provider) error {
	u, err := ad.acmeUser()
	if err != nil {
		return err
	}
	conf := lego.NewConfig(u)
	conf.CADirURL = ad.cfg.ACME
	conf.UserAgent = "pocket-core-installer"
	conf.Certificate.KeyType = certcrypto.EC256
	client, err := lego.NewClient(conf)
	if err != nil {
		return err
	}
	if u.reg == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("Let's Encrypt account: %w", err)
		}
		u.reg = reg
		_ = writeJSONFile(ad.private("acme", "registration.json"), reg, 0o600)
		_ = writeFileAtomic(ad.private("acme", "ca"), []byte(ad.cfg.ACME), 0o600)
	}
	// Only the name servers of the zone are asked (lego's default); public resolvers find them.
	if err := client.Challenge.SetDNS01Provider(p, dns01.AddRecursiveNameservers([]string{"1.1.1.1:53", "8.8.8.8:53"})); err != nil {
		return err
	}
	ad.log.Info("address: asking Let's Encrypt for a certificate", "domain", domain)
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{Domains: []string{domain, "*." + domain}, Bundle: true})
	if err != nil {
		return fmt.Errorf("Let's Encrypt: %w", err)
	}
	crt, key := ad.certPaths(domain)
	if err := writeFileAtomic(key, res.PrivateKey, 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(crt, res.Certificate, 0o600); err != nil {
		return err
	}
	ad.log.Info("address: certificate", "domain", domain)
	return nil
}
