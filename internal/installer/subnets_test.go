package installer

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func cidrs(t *testing.T, s ...string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range s {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// On a machine whose Docker used up its default pools (172.17–172.31 as /16s), the first free
// /24 is in 172.16.0.0/16, clear of the LAN.
func TestFreeSubnet(t *testing.T) {
	used := cidrs(t, "192.168.100.0/24", "172.16.0.0/24")
	for i := 17; i <= 31; i++ {
		used = append(used, cidrs(t, "172."+itoa(i)+".0.0/16")...)
	}
	got := freeSubnet(used)
	if got == nil || got.String() != "172.16.1.0/24" {
		t.Fatalf("free = %v, want 172.16.1.0/24", got)
	}
	// A LAN on all of 172.16/12 sends it to the second range.
	if got := freeSubnet(cidrs(t, "172.16.0.0/12")); got == nil || got.String() != "10.213.0.0/24" {
		t.Fatalf("free = %v, want 10.213.0.0/24", got)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestPinNetworks(t *testing.T) {
	subs := []string{"172.16.1.0/24", "172.16.2.0/24", "172.16.3.0/24"}
	next := func() (string, error) { s := subs[0]; subs = subs[1:]; return s, nil }
	in := `# Pocket X — installed by Pocket Core
services:
  app:
    image: x
  db:
    image: y
    networks: [back]
  host:
    image: z
    network_mode: host
networks:
  back:
  ext:
    external: true
`
	out, n, err := pinNetworks([]byte(in), next)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("pinned %d, want 2 (back + default):\n%s", n, out)
	}
	s := string(out)
	for _, want := range []string{"subnet: 172.16.1.0/24", "subnet: 172.16.2.0/24", "external: true", "# Pocket X"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "subnet:") != 2 {
		t.Fatalf("ext must stay unpinned:\n%s", s)
	}
	// Only host networking: nothing to pin.
	if _, n, _ := pinNetworks([]byte("services:\n  a:\n    image: x\n    network_mode: host\n"), next); n != 0 {
		t.Fatalf("host-only stack pinned %d", n)
	}
}

func TestLeIP(t *testing.T) {
	if ip := leIP("0064A8C0"); ip.String() != "192.168.100.0" {
		t.Fatalf("leIP = %v", ip)
	}
}
