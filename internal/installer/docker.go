package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Docker runs the docker CLI with a clean environment. The agent's own
// environment must not leak into compose: shell variables win over .env for
// ${VAR} in docker-compose.yml.
type Docker struct {
	Bin string
	env []string
}

func NewDocker() *Docker {
	bin, err := exec.LookPath("docker")
	if err != nil {
		bin = "docker"
	}
	d := &Docker{Bin: bin}
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			d.env = append(d.env, k+"="+v)
		}
	}
	return d
}

func (d *Docker) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.Bin, args...)
	cmd.Dir, cmd.Env = dir, d.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	s := strings.TrimSpace(out.String())
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return s, fmt.Errorf("docker %s: %s", strings.Join(args[:min(2, len(args))], " "), lastLines(s, 6))
	}
	return s, nil
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// container is the part of `docker inspect` the agent needs.
type container struct {
	Name   string
	Ports  []int
	Host   bool // network_mode host
	Env    map[string]string
	Labels map[string]string
}

func (c *container) project() (name, dir string, files []string) {
	name = c.Labels["com.docker.compose.project"]
	dir = c.Labels["com.docker.compose.project.working_dir"]
	if f := c.Labels["com.docker.compose.project.config_files"]; f != "" {
		files = strings.Split(f, ",")
	}
	return
}

// containers inspects every container, running or not.
func (d *Docker) containers(ctx context.Context) ([]container, error) {
	ids, err := d.run(ctx, "", "ps", "-aq")
	if err != nil {
		return nil, err
	}
	if ids == "" {
		return nil, nil
	}
	out, err := d.run(ctx, "", append([]string{"inspect"}, strings.Fields(ids)...)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name       string
		HostConfig struct {
			NetworkMode  string
			PortBindings map[string][]struct{ HostPort string }
		}
		Config struct {
			Env    []string
			Labels map[string]string
		}
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	cs := make([]container, 0, len(raw))
	for _, r := range raw {
		c := container{Name: strings.TrimPrefix(r.Name, "/"), Host: r.HostConfig.NetworkMode == "host", Labels: r.Config.Labels, Env: map[string]string{}}
		for _, bs := range r.HostConfig.PortBindings {
			for _, b := range bs {
				if p, err := strconv.Atoi(b.HostPort); err == nil {
					c.Ports = append(c.Ports, p)
				}
			}
		}
		for _, e := range r.Config.Env {
			if k, v, ok := strings.Cut(e, "="); ok {
				c.Env[k] = v
			}
		}
		cs = append(cs, c)
	}
	return cs, nil
}

// portFree reports whether nothing listens on the port, on any address.
func portFree(p int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// dockerGID is the docker group's id, for stacks that mount the socket.
func dockerGID() string {
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Split(line, ":"); len(f) >= 3 && f[0] == "docker" {
			return f[2]
		}
	}
	return ""
}
