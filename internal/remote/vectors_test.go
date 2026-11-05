package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/flynn/noise"
)

// Fixed keys make the handshake deterministic, so other clients (the Swift
// one in Pocket Homelab) can check themselves against testdata/noise_vectors.json.
// UPDATE_VECTORS=1 rewrites the file.
func fixedKey(seed string) noise.DHKey {
	sum := sha256.Sum256([]byte(seed))
	k, _ := noise.DH25519.GenerateKeypair(bytes.NewReader(sum[:]))
	return k
}

type noiseVectors struct {
	Prologue       string   `json:"prologue"`
	BoxPrivate     string   `json:"box_private"`
	BoxPublic      string   `json:"box_public"`
	DevicePrivate  string   `json:"device_private"`
	DevicePublic   string   `json:"device_public"`
	InitEphemeral  string   `json:"initiator_ephemeral_private"`
	RespEphemeral  string   `json:"responder_ephemeral_private"`
	Payload2       string   `json:"payload2"`
	Payload3       string   `json:"payload3"`
	Message1       string   `json:"message1"`
	Message2       string   `json:"message2"`
	Message3       string   `json:"message3"`
	InitToResp     []string `json:"initiator_to_responder"` // plaintext, ciphertext pairs
	RespToInit     []string `json:"responder_to_initiator"`
}

func TestNoiseVectors(t *testing.T) {
	box, dev := fixedKey("box"), fixedKey("device")
	ie, re := fixedKey("initiator-ephemeral"), fixedKey("responder-ephemeral")
	host := "money.remote.example.org"
	// flynn/noise draws ephemerals from Random: feed it the fixed private keys.
	ini, _ := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Pattern: noise.HandshakeXK, Initiator: true,
		Prologue: Prologue(host), StaticKeypair: dev, PeerStatic: box.Public, Random: bytes.NewReader(ie.Private)})
	res, _ := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Pattern: noise.HandshakeXK, Initiator: false,
		Prologue: Prologue(host), StaticKeypair: box, Random: bytes.NewReader(re.Private)})
	p2, p3 := []byte(`{"v":1}`), []byte(`{"t":"open"}`)
	m1, _, _, _ := ini.WriteMessage(nil, nil)
	if _, _, _, err := res.ReadMessage(nil, m1); err != nil {
		t.Fatal(err)
	}
	m2, _, _, _ := res.WriteMessage(nil, p2)
	if _, _, _, err := ini.ReadMessage(nil, m2); err != nil {
		t.Fatal(err)
	}
	m3, isend, irecv, _ := ini.WriteMessage(nil, p3)
	_, rrecv, rsend, err := res.ReadMessage(nil, m3)
	if err != nil || rrecv == nil || rsend == nil {
		t.Fatal(err)
	}
	v := noiseVectors{Prologue: string(Prologue(host)), BoxPrivate: hex.EncodeToString(box.Private), BoxPublic: hex.EncodeToString(box.Public),
		DevicePrivate: hex.EncodeToString(dev.Private), DevicePublic: hex.EncodeToString(dev.Public),
		InitEphemeral: hex.EncodeToString(ie.Private), RespEphemeral: hex.EncodeToString(re.Private),
		Payload2: string(p2), Payload3: string(p3),
		Message1: hex.EncodeToString(m1), Message2: hex.EncodeToString(m2), Message3: hex.EncodeToString(m3)}
	for _, msg := range []string{"first", "second", ""} {
		c, _ := isend.Encrypt(nil, nil, []byte(msg))
		v.InitToResp = append(v.InitToResp, hex.EncodeToString([]byte(msg)), hex.EncodeToString(c))
	}
	for i := 0; i < len(v.InitToResp); i += 2 {
		c, _ := hex.DecodeString(v.InitToResp[i+1])
		if _, err := rrecv.Decrypt(nil, nil, c); err != nil {
			t.Fatal("responder cannot read the initiator")
		}
	}
	for _, msg := range []string{"reply one", "reply two"} {
		c, _ := rsend.Encrypt(nil, nil, []byte(msg))
		if p, err := irecv.Decrypt(nil, nil, c); err != nil || string(p) != msg {
			t.Fatal("initiator cannot read the responder")
		}
		v.RespToInit = append(v.RespToInit, hex.EncodeToString([]byte(msg)), hex.EncodeToString(c))
	}
	got, _ := json.MarshalIndent(v, "", "  ")
	got = append(got, '\n')
	if os.Getenv("UPDATE_VECTORS") == "1" {
		if err := os.WriteFile("testdata/noise_vectors.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/noise_vectors.json")
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("testdata/noise_vectors.json is out of date: run with UPDATE_VECTORS=1")
	}
}
