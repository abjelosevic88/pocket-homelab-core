package installer

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func slugOK(s string) bool { return slugRe.MatchString(s) }

// imageBase strips the tag or digest: ghcr.io/a/b:1.2 → ghcr.io/a/b.
func imageBase(img string) string {
	img, _, _ = strings.Cut(img, "@")
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		img = img[:i]
	}
	return strings.ToLower(img)
}

// mainService finds the service that runs the repo's own image and returns
// its name and container_name.
func mainService(compose []byte, image string) (string, string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return "", "", fmt.Errorf("docker-compose.yml: %w", err)
	}
	if len(doc.Content) == 0 {
		return "", "", fmt.Errorf("docker-compose.yml is empty")
	}
	svcs := mapGet(doc.Content[0], "services")
	if svcs == nil || svcs.Kind != yaml.MappingNode {
		return "", "", fmt.Errorf("docker-compose.yml has no services")
	}
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		svc := svcs.Content[i+1]
		if img := mapGet(svc, "image"); img != nil && imageBase(img.Value) == image {
			ctr := ""
			if c := mapGet(svc, "container_name"); c != nil {
				ctr = c.Value
			}
			return svcs.Content[i].Value, ctr, nil
		}
	}
	return "", "", fmt.Errorf("docker-compose.yml runs no %s service", image)
}

// composeServices lists the service names of a compose file.
func composeServices(compose []byte) map[string]bool {
	out := map[string]bool{}
	var doc yaml.Node
	if yaml.Unmarshal(compose, &doc) != nil || len(doc.Content) == 0 {
		return out
	}
	if svcs := mapGet(doc.Content[0], "services"); svcs != nil && svcs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			out[svcs.Content[i].Value] = true
		}
	}
	return out
}

// Plan is everything Install writes for one app.
type Plan struct {
	Compose   []byte
	Env       []envVar // the .env file, in order
	Port      int      // host port of the app's web interface
	AppURL    string   // how Core reaches it
	PublicURL string
	Container string
	Dirs      []string // bind-mount sources to create first (relative to the folder)
	Notes     []string // things the owner should know (host folders it mounts…)
}

type envVar struct{ Key, Value string }

// PlanInput is what BuildPlan needs besides the release.
type PlanInput struct {
	Release         *Release
	Name            string // display name for the header comment
	PublicURL       string
	CoreURL         string // as seen from a bridged container
	RegistrationKey string
	AdminEmail      string
	AdminName       string
	TZ              string
	Zoneinfo        bool                // bind /usr/share/zoneinfo (TZ alone is UTC on alpine/distroless)
	NextPort        func() (int, error) // hands out free host ports
	DockerGID       string
	Now             time.Time
}

// BuildPlan turns the repo's sample docker-compose.yml into this server's:
// pinned release image, free host ports, the public address, and Core's
// variables so the app registers itself on its first start.
func BuildPlan(in PlanInput) (*Plan, error) {
	rel := in.Release
	var doc yaml.Node
	if err := yaml.Unmarshal(rel.compose, &doc); err != nil {
		return nil, fmt.Errorf("docker-compose.yml: %w", err)
	}
	root := doc.Content[0]
	svcs := mapGet(root, "services")
	mainName, _, err := mainService(rel.compose, rel.Image)
	if err != nil {
		return nil, err
	}
	p := &Plan{PublicURL: in.PublicURL, Container: rel.Container}

	// Ports first: every published port in the stack gets a free host port,
	// so nothing collides with what already runs here.
	var mainPorts []int
	for i := 0; i+1 < len(svcs.Content); i += 2 {
		name, svc := svcs.Content[i].Value, svcs.Content[i+1]
		got, err := remapPorts(svc, in.NextPort)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		if name == mainName {
			mainPorts = got
		}
		p.Dirs = append(p.Dirs, bindDirs(svc, &p.Notes)...)
	}
	svc := mapGet(svcs, mainName)
	mapSet(svc, "image", str(rel.ref()))
	mapDel(svc, "build")
	if mapGet(svc, "container_name") == nil {
		mapSet(svc, "container_name", str(rel.Container))
	}

	hostNet := false
	if nm := mapGet(svc, "network_mode"); nm != nil && nm.Value == "host" {
		hostNet = true
	}
	envs := envMap(svc)
	coreURL := in.CoreURL
	switch {
	case hostNet:
		// Host networking: the app listens on PORT itself and reaches Core on loopback.
		if p.Port, err = in.NextPort(); err != nil {
			return nil, err
		}
		mapSet(envs, "PORT", str(strconv.Itoa(p.Port)))
		if u, err := url.Parse(in.CoreURL); err == nil && u.Port() != "" {
			coreURL = "http://127.0.0.1:" + u.Port()
		}
	case len(mainPorts) == 0:
		return nil, fmt.Errorf("service %s publishes no port", mainName)
	default:
		p.Port = mainPorts[0]
	}
	p.AppURL = fmt.Sprintf("http://host.docker.internal:%d", p.Port)
	in.PublicURL = strings.ReplaceAll(in.PublicURL, "{port}", strconv.Itoa(p.Port))
	p.PublicURL = in.PublicURL

	secure := strings.HasPrefix(in.PublicURL, "https://")
	for _, kv := range [][2]string{
		{"TZ", in.TZ},
		{"BASE_URL", in.PublicURL},
		{"SECURE_COOKIES", strconv.FormatBool(secure)},
		{"ADMIN_EMAIL", "${ADMIN_EMAIL}"},
		{"ADMIN_PASSWORD", "${ADMIN_PASSWORD}"},
		{"ADMIN_NAME", "${ADMIN_NAME}"},
		{"CORE_URL", coreURL},
		{"CORE_REGISTRATION_KEY", "${CORE_REGISTRATION_KEY}"},
		{"CORE_APP_URL", p.AppURL},
	} {
		mapSet(envs, kv[0], str(kv[1]))
	}
	if !hostNet {
		addExtraHost(svc, "host.docker.internal", "host-gateway")
	}
	if in.Zoneinfo {
		addVolume(svc, "/usr/share/zoneinfo:/usr/share/zoneinfo:ro")
	}

	var buf bytes.Buffer
	title := in.Name
	if title == "" {
		title = displayName(rel.Slug)
	}
	fmt.Fprintf(&buf, "# %s — installed by Pocket Core on %s from %s (%s).\n", title, in.Now.Format("2006-01-02"), rel.RepoURL, rel.Tag)
	fmt.Fprintf(&buf, "# %s, host port %d. Data lives next to this file; settings and secrets are in .env.\n", in.PublicURL, p.Port)
	buf.WriteString("# Update: Pocket Core → Apps → Install app → Update, or change the image tag below and `docker compose up -d`.\n")
	buf.WriteString("# Remove: Pocket Core → Apps → this app → Remove (stops it and archives this folder).\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	enc.Close()
	p.Compose = buf.Bytes()

	// .env: the owner, Core's key, and a fresh value for every secret the
	// stack reads (the samples' defaults are public).
	name := in.AdminName
	if name == "" {
		name = "Admin"
	}
	p.Env = []envVar{
		{"ADMIN_EMAIL", in.AdminEmail},
		{"ADMIN_PASSWORD", randHex(16)},
		{"ADMIN_NAME", name},
		{"CORE_REGISTRATION_KEY", in.RegistrationKey},
	}
	have := map[string]bool{}
	for _, e := range p.Env {
		have[e.Key] = true
	}
	for _, v := range composeVars(p.Compose, rel.envExample) {
		switch {
		case have[v]:
		case v == "DOCKER_GID" && in.DockerGID != "":
			p.Env = append(p.Env, envVar{v, in.DockerGID})
		case v == "TZ":
			p.Env = append(p.Env, envVar{v, in.TZ})
		case looksSecret(v):
			p.Env = append(p.Env, envVar{v, randHex(24)})
		default:
			continue
		}
		have[v] = true
	}
	return p, nil
}

func (p *Plan) envFile() []byte {
	var b bytes.Buffer
	b.WriteString("# Written by Pocket Core's installer. ADMIN_* only seed the app's first account;\n# people sign in with Pocket Core.\n")
	for _, e := range p.Env {
		fmt.Fprintf(&b, "%s=%s\n", e.Key, e.Value)
	}
	return b.Bytes()
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)

// composeVars lists the variables the compose file reads plus the keys the
// sample .env sets, sorted.
func composeVars(compose, envExample []byte) []string {
	seen := map[string]bool{}
	for _, m := range varRe.FindAllSubmatch(compose, -1) {
		seen[string(m[1])] = true
	}
	sc := bufio.NewScanner(bytes.NewReader(envExample))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if k, _, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			seen[strings.TrimSpace(strings.TrimPrefix(k, "export "))] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func looksSecret(k string) bool {
	k = strings.ToUpper(k)
	return strings.Contains(k, "SECRET") || strings.Contains(k, "PASSWORD") || strings.HasSuffix(k, "_KEY") && !strings.Contains(k, "API_KEY") || strings.HasSuffix(k, "_TOKEN") && !strings.Contains(k, "API")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func displayName(slug string) string {
	return "Pocket " + strings.ToUpper(slug[:1]) + strings.ReplaceAll(slug[1:], "-", " ")
}

// ---- ports ----

// remapPorts gives each published port of a service a free host port and
// returns them in order. Container ports stay as they are.
func remapPorts(svc *yaml.Node, next func() (int, error)) ([]int, error) {
	ports := mapGet(svc, "ports")
	if ports == nil || ports.Kind != yaml.SequenceNode {
		return nil, nil
	}
	var got []int
	for _, it := range ports.Content {
		hp, err := next()
		if err != nil {
			return nil, err
		}
		switch it.Kind {
		case yaml.ScalarNode:
			v := it.Value
			proto := ""
			if i := strings.Index(v, "/"); i >= 0 {
				v, proto = v[:i], v[i:]
			}
			parts := strings.Split(v, ":")
			target := parts[len(parts)-1]
			if strings.Contains(target, "-") {
				return nil, fmt.Errorf("port ranges are not supported (%s)", it.Value)
			}
			lc := it.LineComment
			*it = *str(fmt.Sprintf("%d:%s%s", hp, target, proto))
			it.LineComment = lc
		case yaml.MappingNode:
			mapSet(it, "published", str(strconv.Itoa(hp)))
			mapDel(it, "host_ip")
		default:
			return nil, fmt.Errorf("unreadable port entry")
		}
		got = append(got, hp)
	}
	return got, nil
}

// ---- volumes ----

// bindDirs returns the service's relative bind-mount sources. Docker would
// create missing ones as root, which the app (uid 1000) then cannot write.
func bindDirs(svc *yaml.Node, notes *[]string) []string {
	vols := mapGet(svc, "volumes")
	if vols == nil || vols.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, it := range vols.Content {
		src := ""
		switch it.Kind {
		case yaml.ScalarNode:
			src = volumeSource(it.Value)
		case yaml.MappingNode:
			if t := mapGet(it, "type"); t != nil && t.Value == "bind" {
				if s := mapGet(it, "source"); s != nil {
					src = s.Value
				}
			}
		}
		// ${MEDIA_DIR:-./media}: the .env this installer writes leaves such
		// folders unset, so the default is what Docker mounts.
		if strings.HasPrefix(src, "${") && strings.HasSuffix(src, "}") {
			if _, def, ok := strings.Cut(src[2:len(src)-1], ":-"); ok && (strings.HasPrefix(def, "./") || strings.HasPrefix(def, "../")) {
				src = def
			}
		}
		switch {
		case strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../"):
			if c := filepath.Clean(src); !strings.HasPrefix(c, "..") && c != "." {
				out = append(out, c)
			}
		case strings.HasPrefix(src, "/") && src != "/usr/share/zoneinfo":
			*notes = append(*notes, "mounts "+src+" from the host")
		}
	}
	return out
}

func addVolume(svc *yaml.Node, v string) {
	vols := mapGet(svc, "volumes")
	if vols == nil {
		vols = &yaml.Node{Kind: yaml.SequenceNode}
		mapSet(svc, "volumes", vols)
	}
	target := strings.Split(v, ":")[1]
	for _, it := range vols.Content {
		if it.Kind == yaml.ScalarNode && len(strings.Split(it.Value, ":")) > 1 && strings.Split(it.Value, ":")[1] == target {
			return
		}
	}
	vols.Content = append(vols.Content, str(v))
}

func addExtraHost(svc *yaml.Node, host, addr string) {
	eh := mapGet(svc, "extra_hosts")
	if eh == nil {
		eh = &yaml.Node{Kind: yaml.SequenceNode}
		mapSet(svc, "extra_hosts", eh)
	}
	if eh.Kind == yaml.MappingNode {
		mapSet(eh, host, str(addr))
		return
	}
	for _, it := range eh.Content {
		if h, _, _ := strings.Cut(it.Value, ":"); h == host {
			return
		}
	}
	eh.Content = append(eh.Content, str(host+":"+addr))
}

// envMap returns the service's environment as a mapping node, converting a
// KEY=value list in place.
func envMap(svc *yaml.Node) *yaml.Node {
	e := mapGet(svc, "environment")
	if e == nil {
		e = &yaml.Node{Kind: yaml.MappingNode}
		mapSet(svc, "environment", e)
		return e
	}
	if e.Kind == yaml.SequenceNode {
		m := &yaml.Node{Kind: yaml.MappingNode}
		for _, it := range e.Content {
			k, v, ok := strings.Cut(it.Value, "=")
			if !ok {
				v = "${" + k + "}"
			}
			mapSet(m, k, str(v))
		}
		*e = *m
	}
	return e
}

// ---- yaml.Node helpers ----

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// keep the comment that explained the old value
			v.LineComment = m.Content[i+1].LineComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, str(key), v)
}

func mapDel(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// writePlan creates the app folder. It refuses to touch one that exists.
// Run as root, it gives the data folders to the app's uid and the rest to
// the person (see Config.AppOwner/FilesOwner).
func writePlan(dir string, p *Plan, app, files owner) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files.chown(dir)
	for _, d := range p.Dirs {
		full := filepath.Join(dir, d)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return err
		}
		for x := full; x != dir && strings.HasPrefix(x, dir); x = filepath.Dir(x) {
			app.chown(x)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), p.envFile(), 0o600); err != nil {
		return err
	}
	files.chown(filepath.Join(dir, ".env"))
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), p.Compose, 0o644); err != nil {
		return err
	}
	files.chown(filepath.Join(dir, "docker-compose.yml"))
	return nil
}
