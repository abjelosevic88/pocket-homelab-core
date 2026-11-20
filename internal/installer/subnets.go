package installer

// Every app gets its own Docker network, and Docker hands each one a range from its address pools.
// With Docker's defaults those run out after about 31 networks ("all predefined address pools have
// been fully subnetted"), so the 32nd app on a fresh machine would not start. install.sh gives
// Docker more room, but not every machine went through that (installs from before, a "no" to
// restarting Docker, Docker Desktop, a NAS). So when Docker says it is out, the installer picks a
// free /24 itself and writes it into the app's compose file: nothing on the machine changes, and
// the app keeps its range from then on. The agent runs on the host's network, so it sees the
// machine's own routes as well as Docker's networks and stays clear of both.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Where the installer looks for a free /24, in order: the private range Docker uses itself, then a
// quiet corner of 10/8.
var subnetRanges = []string{"172.16.0.0/12", "10.213.0.0/16"}

func outOfPools(err error) bool {
	return err != nil && strings.Contains(err.Error(), "fully subnetted")
}

// upPinned starts the app on networks of its own choosing, after Docker ran out of ranges.
func (a *Agent) upPinned(ctx context.Context, j *Job, dir string) error {
	file := filepath.Join(dir, "docker-compose.yml")
	orig, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	used := append(a.dockerSubnets(ctx), hostRoutes()...)
	var last error
	for try := 0; try < 3; try++ {
		var picked []string
		next := func() (string, error) {
			s := freeSubnet(used)
			if s == nil {
				return "", errors.New("no free /24 left in " + strings.Join(subnetRanges, " or "))
			}
			used = append(used, s)
			picked = append(picked, s.String())
			return s.String(), nil
		}
		out, n, err := pinNetworks(orig, next)
		if err != nil {
			return err
		}
		if n == 0 {
			return errors.New("the app's networks can't be given a range (external or not bridge)")
		}
		if err := os.WriteFile(file, out, 0o644); err != nil {
			return err
		}
		a.logf(j, "Docker has no network ranges left; this app gets its own: %s", strings.Join(picked, ", "))
		if _, last = a.dk.run(ctx, dir, "compose", "up", "-d"); last == nil {
			return nil
		}
		if !strings.Contains(last.Error(), "overlap") {
			return last
		}
		// Something we couldn't see holds that range: clear what this try made and pick again.
		_, _ = a.dk.run(ctx, dir, "compose", "down", "--remove-orphans")
	}
	return last
}

// dockerSubnets is every IPv4 range Docker's networks hold.
func (a *Agent) dockerSubnets(ctx context.Context) []*net.IPNet {
	ids, err := a.dk.run(ctx, "", "network", "ls", "-q")
	if err != nil || strings.TrimSpace(ids) == "" {
		return nil
	}
	args := append([]string{"network", "inspect", "-f", "{{range .IPAM.Config}}{{.Subnet}} {{end}}"}, strings.Fields(ids)...)
	out, err := a.dk.run(ctx, "", args...)
	if err != nil {
		return nil
	}
	var nets []*net.IPNet
	for _, f := range strings.Fields(out) {
		if _, n, err := net.ParseCIDR(f); err == nil && n.IP.To4() != nil {
			nets = append(nets, n)
		}
	}
	return nets
}

// hostRoutes is the machine's IPv4 routes (the LAN, VPNs, Docker's bridges), not the default route.
func hostRoutes() []*net.IPNet {
	fh, err := os.Open("/proc/net/route")
	if err != nil {
		return nil
	}
	defer fh.Close()
	var nets []*net.IPNet
	sc := bufio.NewScanner(fh)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		dst, mask := leIP(f[1]), leIP(f[7])
		if dst == nil || mask == nil {
			continue
		}
		if ones, _ := net.IPMask(mask).Size(); ones == 0 {
			continue
		}
		nets = append(nets, &net.IPNet{IP: dst.Mask(net.IPMask(mask)), Mask: net.IPMask(mask)})
	}
	return nets
}

// leIP reads /proc/net/route's little-endian hex address.
func leIP(h string) net.IP {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 4 {
		return nil
	}
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, binary.LittleEndian.Uint32(b))
	return ip
}

// freeSubnet is the first /24 of subnetRanges that overlaps nothing in used.
func freeSubnet(used []*net.IPNet) *net.IPNet {
	for _, r := range subnetRanges {
		_, base, err := net.ParseCIDR(r)
		if err != nil {
			continue
		}
		start := binary.BigEndian.Uint32(base.IP.To4())
		ones, _ := base.Mask.Size()
		count := uint32(1) << (24 - ones)
		for i := uint32(0); i < count; i++ {
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, start+i<<8)
			cand := &net.IPNet{IP: ip, Mask: net.CIDRMask(24, 32)}
			if !overlapsAny(cand, used) {
				return cand
			}
		}
	}
	return nil
}

func overlapsAny(n *net.IPNet, list []*net.IPNet) bool {
	for _, u := range list {
		if u.Contains(n.IP) || n.Contains(u.IP) {
			return true
		}
	}
	return false
}

// pinNetworks gives each network the stack creates (its default one included) a range from next,
// unless it is external, already has one, or isn't a bridge. It returns how many it pinned.
func pinNetworks(compose []byte, next func() (string, error)) ([]byte, int, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, 0, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, 0, errors.New("compose file is not a mapping")
	}
	root := doc.Content[0]
	nets := mapGet(root, "networks")
	if nets == nil || nets.Kind != yaml.MappingNode {
		nets = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(root, "networks", nets)
	}
	// The default network exists when a service names no networks and doesn't share another's.
	if svcs := mapGet(root, "services"); svcs != nil && mapGet(nets, "default") == nil {
		for i := 0; i+1 < len(svcs.Content); i += 2 {
			s := svcs.Content[i+1]
			if mapGet(s, "network_mode") == nil && mapGet(s, "networks") == nil {
				mapSet(nets, "default", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
				break
			}
		}
	}
	n := 0
	for i := 0; i+1 < len(nets.Content); i += 2 {
		v := nets.Content[i+1]
		if v.Kind != yaml.MappingNode { // `default:` with nothing after it
			v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			nets.Content[i+1] = v
		}
		if ext := mapGet(v, "external"); ext != nil && ext.Value != "false" || mapGet(v, "ipam") != nil {
			continue
		}
		if d := mapGet(v, "driver"); d != nil && d.Value != "bridge" {
			continue
		}
		sub, err := next()
		if err != nil {
			return nil, 0, err
		}
		cfg := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(cfg, "subnet", str(sub))
		ipam := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mapSet(ipam, "config", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{cfg}})
		mapSet(v, "ipam", ipam)
		n++
	}
	if n == 0 {
		return compose, 0, nil
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, 0, fmt.Errorf("compose file: %w", err)
	}
	enc.Close()
	return []byte(buf.String()), n, nil
}
