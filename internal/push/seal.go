// Package push seals notifications for one Pocket Hub install and hands them to the Pocket Push
// relay, which forwards them to Apple (APNs). The relay never sees the text: each message is
// sealed to the device's own X25519 key (see docs/push.md, "Sealing").
package push

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	sealVersion = 0x01
	sealInfo    = "pocket-push v1"
	keyLen      = 32
	nonceLen    = chacha20poly1305.NonceSize
	tagLen      = chacha20poly1305.Overhead

	// MaxTitle and MaxBody clip a message (in runes) so the sealed blob stays under ~3000 bytes.
	MaxTitle = 120
	MaxBody  = 900
)

// Message is what a Pocket Hub install shows. Every field but V is optional.
type Message struct {
	V      int    `json:"v"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url,omitempty"`
	Source string `json:"source,omitempty"`
	ID     string `json:"id,omitempty"`
	At     string `json:"at,omitempty"`
	Sound  bool   `json:"sound,omitempty"`
	Thread string `json:"thread,omitempty"`
}

// Clip shortens title and body to MaxTitle / MaxBody runes and sets V.
func (m *Message) Clip() {
	m.V = 1
	m.Title = clip(m.Title, MaxTitle)
	m.Body = clip(m.Body, MaxBody)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// ParsePublicKey checks a device public key: standard base64 of exactly 32 bytes.
func ParsePublicKey(b64 string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != keyLen {
		return nil, errors.New("public_key must be the standard base64 of 32 bytes")
	}
	if _, err := ecdh.X25519().NewPublicKey(b); err != nil {
		return nil, err
	}
	return b, nil
}

// SealMessage clips m and seals its JSON for the device public key (base64).
func SealMessage(devicePubB64 string, m Message) (string, error) {
	m.Clip()
	pt, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	pub, err := ParsePublicKey(devicePubB64)
	if err != nil {
		return "", err
	}
	return Seal(pub, pt)
}

// Seal encrypts plaintext for a device's raw X25519 public key:
// wire = base64(0x01 ‖ e_pub(32) ‖ nonce(12) ‖ ciphertext ‖ tag(16)), with
// key = HKDF-SHA256(X25519(e, device_pub), salt = e_pub ‖ device_pub, info "pocket-push v1").
func Seal(devicePub, plaintext []byte) (string, error) {
	dp, err := ecdh.X25519().NewPublicKey(devicePub)
	if err != nil {
		return "", err
	}
	e, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealWith(e, dp, nonce, plaintext)
}

func sealWith(e *ecdh.PrivateKey, dp *ecdh.PublicKey, nonce, plaintext []byte) (string, error) {
	aead, err := deriveAEAD(e, dp, e.PublicKey().Bytes(), dp.Bytes())
	if err != nil {
		return "", err
	}
	out := make([]byte, 0, 1+keyLen+nonceLen+len(plaintext)+tagLen)
	out = append(out, sealVersion)
	out = append(out, e.PublicKey().Bytes()...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open is the device side of Seal (Core only uses it in tests; Pocket Hub does the same in Swift).
func Open(devicePriv []byte, sealed string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	if len(raw) < 1+keyLen+nonceLen+tagLen || raw[0] != sealVersion {
		return nil, errors.New("not a pocket-push v1 blob")
	}
	priv, err := ecdh.X25519().NewPrivateKey(devicePriv)
	if err != nil {
		return nil, err
	}
	ePub, err := ecdh.X25519().NewPublicKey(raw[1 : 1+keyLen])
	if err != nil {
		return nil, err
	}
	aead, err := deriveAEAD(priv, ePub, ePub.Bytes(), priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	nonce := raw[1+keyLen : 1+keyLen+nonceLen]
	return aead.Open(nil, nonce, raw[1+keyLen+nonceLen:], nil)
}

func deriveAEAD(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, ePub, devPub []byte) (cipher.AEAD, error) {
	shared, err := priv.ECDH(peer)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 0, 2*keyLen)
	salt = append(salt, ePub...)
	salt = append(salt, devPub...)
	key, err := hkdf.Key(sha256.New, shared, salt, sealInfo, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return chacha20poly1305.New(key)
}
