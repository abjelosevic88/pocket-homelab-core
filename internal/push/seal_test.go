package push

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The contract's test vector (pocket-push v1): Pocket Hub's Swift side decrypts the same blob.
const (
	vecDevicePriv = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	vecDevicePub  = "B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9/AsrhtHHw="
	vecSealed     = "AXmmMe7eG/nJjxIDLN6t0OegeTmPx4a4jMhG7ImvhaUaoKGio6SlpqeoqaqrtebxRBoyA66kW78oXFz/WRvrd/8Qoux7ZypPIyOt2tRnJNFQJlk6EGRctzAMq2B+muNvxSbNG0+2n/oBZTF9x1EhU9dLUv0Vj7YKiEz3lQ3w18ogV18VYY3/utqRJJ/9YZEDJRxj8pAMacUSdXlHX/Z4Hft7ZNDhKxTs9yTwAW8zsMCsZBiqOp9/18xfNZp4FI7IodiYU+t1grEA/Up9dQaWspYJeeiDxOO4/V9PghL4jys/U978Ee++qXA0s1tLmEQiZvVYbPbeka1V7XpF"
	vecPlain      = `{"at":"2026-11-12T11:00:00Z","body":"Nightly backup took 4 min — ✅","id":"42","sound":true,"source":"Backups","title":"Backup finished","url":"https://alerts.example.com/alerts/42","v":1}`
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenContractVector(t *testing.T) {
	priv := b64(t, vecDevicePriv)
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()); got != vecDevicePub {
		t.Fatalf("public key %s", got)
	}
	pt, err := Open(priv, vecSealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != vecPlain {
		t.Fatalf("plaintext %s", pt)
	}

	// Rebuilding the vector with its fixed ephemeral key and nonce gives the same bytes.
	eph := make([]byte, 32)
	nonce := make([]byte, 12)
	for i := range eph {
		eph[i] = byte(0x40 + i)
	}
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	e, _ := ecdh.X25519().NewPrivateKey(eph)
	got, err := sealWith(e, k.PublicKey(), nonce, []byte(vecPlain))
	if err != nil || got != vecSealed {
		t.Fatalf("sealWith differs: %v", err)
	}

	// A flipped byte or the wrong key fails.
	raw := b64(t, vecSealed)
	raw[len(raw)-1] ^= 1
	if _, err := Open(priv, base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered blob opened")
	}
	other := make([]byte, 32)
	_, _ = rand.Read(other)
	if _, err := Open(other, vecSealed); err == nil {
		t.Fatal("wrong key opened")
	}
}

func TestSealRoundTrip(t *testing.T) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
	m := Message{Title: strings.Repeat("T", 300), Body: strings.Repeat("ž", 2000), URL: "https://alerts.example.com/a/1", Source: "Backups", ID: "7", Sound: true, Thread: "alerts"}
	sealed, err := SealMessage(pub, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) > 3000 {
		t.Fatalf("sealed blob is %d bytes", len(sealed))
	}
	pt, err := Open(k.Bytes(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	var back Message
	if err := json.Unmarshal(pt, &back); err != nil {
		t.Fatal(err)
	}
	if back.V != 1 || utf8.RuneCountInString(back.Title) != MaxTitle || utf8.RuneCountInString(back.Body) != MaxBody || back.Thread != "alerts" || !back.Sound || back.ID != "7" {
		t.Fatalf("round trip %+v", back)
	}
	// Two seals of the same message differ (fresh ephemeral key and nonce).
	again, _ := SealMessage(pub, m)
	if again == sealed {
		t.Fatal("seal is deterministic")
	}
	if _, err := ParsePublicKey(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("31-byte key accepted")
	}
}
