package installer

// Tailscale for an install.sh install: reaching the apps from a phone or laptop away from home.
// install.sh's compose file has a Tailscale container (profile "tailscale", host networking);
// "Connect" starts it and hands back Tailscale's sign-in link, "Disconnect" logs it out and stops it.
// A machine that already runs Tailscale itself is left alone and only reported.
//
//	GET  /v1/tailscale                 {state, name, ips, auth_url, managed}
//	POST /v1/tailscale/up {authkey?}   start it; the sign-in link, or connected with an auth key
//	POST /v1/tailscale/down            log out and stop it

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type tsStatus struct {
	// off | host (Tailscale runs on this machine outside Pocket) | starting | needs-login | running
	State   string   `json:"state"`
	Name    string   `json:"name,omitempty"` // the machine's name on the tailnet (MagicDNS)
	IPs     []string `json:"ips,omitempty"`
	AuthURL string   `json:"auth_url,omitempty"`
	Managed bool     `json:"managed"` // Pocket's own Tailscale container
}

func (ad *Address) tsContainer() string {
	if ad.cfg.Tailscale != "" {
		return ad.cfg.Tailscale
	}
	return "pocket-tailscale"
}

func (ad *Address) tsRunning(ctx context.Context) bool {
	out, err := ad.ag.dk.run(ctx, "", "inspect", "-f", "{{.State.Running}}", ad.tsContainer())
	return err == nil && strings.TrimSpace(out) == "true"
}

// hostTailscale: the addresses of a tailscale0 interface this machine has outside Pocket's container.
func hostTailscale() []string {
	ifc, err := net.InterfaceByName("tailscale0")
	if err != nil {
		return nil
	}
	addrs, _ := ifc.Addrs()
	var ips []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP.String())
		}
	}
	return ips
}

func (ad *Address) tsStatus(ctx context.Context) tsStatus {
	if !ad.tsRunning(ctx) {
		if ips := hostTailscale(); len(ips) > 0 {
			return tsStatus{State: "host", IPs: ips}
		}
		return tsStatus{State: "off"}
	}
	out, err := ad.ag.dk.run(ctx, "", "exec", ad.tsContainer(), "tailscale", "status", "--json")
	if err != nil && !strings.HasPrefix(strings.TrimSpace(out), "{") {
		return tsStatus{State: "starting", Managed: true}
	}
	var raw struct {
		BackendState string
		AuthURL      string
		Self         *struct {
			DNSName      string
			HostName     string
			TailscaleIPs []string
		}
	}
	if json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &raw) != nil {
		return tsStatus{State: "starting", Managed: true}
	}
	st := tsStatus{Managed: true, AuthURL: raw.AuthURL}
	switch raw.BackendState {
	case "Running":
		st.State = "running"
	case "NeedsLogin", "NeedsMachineAuth":
		st.State = "needs-login"
	default:
		st.State = "starting"
	}
	if raw.Self != nil {
		st.Name = strings.TrimSuffix(raw.Self.DNSName, ".")
		st.IPs = raw.Self.TailscaleIPs
	}
	return st
}

func (ad *Address) tsUp(ctx context.Context, authkey string) (tsStatus, error) {
	if st := ad.tsStatus(ctx); st.State == "host" {
		return st, problemf("Tailscale already runs on this machine (%s), so the apps are reachable over it as they are.", strings.Join(st.IPs, ", "))
	} else if st.State == "running" {
		return st, nil
	}
	if err := ad.envSet(map[string]string{"COMPOSE_PROFILES": profiles(ad.envGet("COMPOSE_PROFILES"), "tailscale", "")}); err != nil {
		return tsStatus{}, err
	}
	if _, err := ad.compose(ctx, "--profile", "tailscale", "up", "-d", "tailscale"); err != nil {
		return tsStatus{}, problemf("Tailscale didn't start: %v", err)
	}
	host, _ := os.Hostname()
	args := []string{"exec"}
	if authkey == "" {
		args = append(args, "-d") // `tailscale up` waits for the sign-in; its link shows in the status
	}
	args = append(args, ad.tsContainer(), "tailscale", "up", "--hostname="+firstNonEmpty(ad.envGet("TS_HOSTNAME"), "pocket-"+host))
	if authkey != "" {
		args = append(args, "--authkey="+authkey)
	}
	// The daemon needs a moment after the container starts.
	var err error
	for i := 0; i < 10; i++ {
		if _, err = ad.ag.dk.run(ctx, "", args...); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return tsStatus{}, problemf("Tailscale didn't connect: %v", err)
	}
	for i := 0; i < 15; i++ { // until it has a sign-in link or is connected
		st := ad.tsStatus(ctx)
		if st.State == "running" || st.AuthURL != "" {
			return st, nil
		}
		time.Sleep(time.Second)
	}
	return ad.tsStatus(ctx), nil
}

func (ad *Address) tsDown(ctx context.Context) (tsStatus, error) {
	if ad.tsRunning(ctx) {
		_, _ = ad.ag.dk.run(ctx, "", "exec", ad.tsContainer(), "tailscale", "logout")
	}
	_, _ = ad.compose(ctx, "--profile", "tailscale", "rm", "-sf", "tailscale")
	_ = os.RemoveAll(ad.cfg.Dir + "/tailscale")
	if err := ad.envSet(map[string]string{"COMPOSE_PROFILES": profiles(ad.envGet("COMPOSE_PROFILES"), "", "tailscale")}); err != nil {
		return tsStatus{}, err
	}
	return ad.tsStatus(ctx), nil
}

func (ad *Address) tailscaleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/tailscale", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ad.tsStatus(r.Context()))
	})
	mux.HandleFunc("POST /v1/tailscale/up", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			AuthKey string `json:"authkey"`
		}
		_ = decodeBody(r, &b)
		st, err := ad.tsUp(r.Context(), strings.TrimSpace(b.AuthKey))
		ad.reply(w, st, err)
	})
	mux.HandleFunc("POST /v1/tailscale/down", func(w http.ResponseWriter, r *http.Request) {
		st, err := ad.tsDown(r.Context())
		ad.reply(w, st, err)
	})
}
