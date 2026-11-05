package remote

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestNoiseInteropWithBrowserCode runs assets/noise.js under Node (same
// WebCrypto as browsers) against the Go responder Core uses.
func TestNoiseInteropWithBrowserCode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	box, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "testdata/noise_peer.mjs", hex.EncodeToString(box.Public), "Money.Remote.example.org")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	read := func(tag string) string {
		if !sc.Scan() {
			t.Fatalf("node ended before %q: %s", tag, stderr.String())
		}
		v, ok := strings.CutPrefix(sc.Text(), tag+" ")
		if !ok {
			t.Fatalf("want %q, got %q", tag, sc.Text())
		}
		return v
	}
	unhex := func(s string) []byte { b, _ := hex.DecodeString(s); return b }

	static := read("static")
	r, err := NewResponder(box, "money.remote.example.org")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := r.Accept(unhex(read("m1")), []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(stdin, hex.EncodeToString(m2))
	if got := read("reply"); got != `{"v":1}` {
		t.Fatalf("reply %q", got)
	}
	payload, peer, sess, err := r.Finish(unhex(read("m3")))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"t":"open"}` || hex.EncodeToString(peer) != static {
		t.Fatalf("payload %q peer %x want %s", payload, peer, static)
	}
	for i := 0; i < 3; i++ {
		p, err := sess.Decrypt(unhex(read("c")))
		if err != nil || string(p) != fmt.Sprintf("from js %d", i) {
			t.Fatalf("decrypt %d: %q %v", i, p, err)
		}
	}
	for i := 0; i < 3; i++ {
		c, _ := sess.Encrypt([]byte(fmt.Sprintf("from go %d", i)))
		fmt.Fprintln(stdin, hex.EncodeToString(c))
		if got := read("p"); got != fmt.Sprintf("from go %d", i) {
			t.Fatalf("node decrypted %q", got)
		}
	}
}

// A different address in the prologue must break the handshake.
func TestNoisePrologueBindsHost(t *testing.T) {
	box, _ := GenerateKey()
	dev, _ := GenerateKey()
	i, _ := NewInitiator(dev, box.Public, "money.remote.example.org")
	r, _ := NewResponder(box, "ai.remote.example.org")
	m1, _ := i.Start()
	m2, err := r.Accept(m1, nil)
	if err == nil {
		if _, _, _, err = i.Finish(m2, nil); err == nil {
			t.Fatal("handshake across two hosts succeeded")
		}
	}
}
