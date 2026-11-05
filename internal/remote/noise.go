package remote

import (
	"crypto/rand"
	"errors"
	"strings"
	"sync"

	"github.com/flynn/noise"
)

// Suite is Noise_XK_25519_AESGCM_SHA256; the browser implements the same
// pattern on WebCrypto (assets/noise.js).
var Suite = noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256)

// Key is an X25519 key pair.
type Key = noise.DHKey

// GenerateKey makes a new X25519 key pair.
func GenerateKey() (Key, error) { return noise.DH25519.GenerateKeypair(rand.Reader) }

// Prologue binds a session to the address the browser opened.
func Prologue(host string) []byte {
	return []byte("pocket-remote/1 " + strings.ToLower(host))
}

// Responder is Core's half of one handshake.
type Responder struct {
	hs   *noise.HandshakeState
	step int
}

func NewResponder(key Key, host string) (*Responder, error) {
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Random: rand.Reader,
		Pattern: noise.HandshakeXK, Initiator: false, Prologue: Prologue(host), StaticKeypair: key})
	if err != nil {
		return nil, err
	}
	return &Responder{hs: hs}, nil
}

var ErrHandshakeOrder = errors.New("remote: handshake message out of order")

// Accept reads `→ e, es` and returns `← e, ee` carrying payload.
func (r *Responder) Accept(msg1, payload []byte) ([]byte, error) {
	if r.step != 0 {
		return nil, ErrHandshakeOrder
	}
	if _, _, _, err := r.hs.ReadMessage(nil, msg1); err != nil {
		return nil, err
	}
	out, _, _, err := r.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, err
	}
	r.step = 1
	return out, nil
}

// Finish reads `→ s, se` and returns its payload, the browser's static key
// and the session.
func (r *Responder) Finish(msg3 []byte) ([]byte, []byte, *Session, error) {
	if r.step != 1 {
		return nil, nil, nil, ErrHandshakeOrder
	}
	payload, recv, send, err := r.hs.ReadMessage(nil, msg3)
	if err != nil {
		return nil, nil, nil, err
	}
	if recv == nil || send == nil {
		return nil, nil, nil, ErrHandshakeOrder
	}
	r.step = 2
	return payload, append([]byte(nil), r.hs.PeerStatic()...), &Session{send: send, recv: recv}, nil
}

// Initiator is the browser's half, used by tests and Go clients.
type Initiator struct {
	hs   *noise.HandshakeState
	step int
}

func NewInitiator(key Key, boxPublic []byte, host string) (*Initiator, error) {
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: Suite, Random: rand.Reader,
		Pattern: noise.HandshakeXK, Initiator: true, Prologue: Prologue(host), StaticKeypair: key, PeerStatic: boxPublic})
	if err != nil {
		return nil, err
	}
	return &Initiator{hs: hs}, nil
}

// Start writes `→ e, es`.
func (i *Initiator) Start() ([]byte, error) {
	out, _, _, err := i.hs.WriteMessage(nil, nil)
	i.step = 1
	return out, err
}

// Finish reads `← e, ee` and writes `→ s, se` with payload.
func (i *Initiator) Finish(msg2, payload []byte) (reply, msg3 []byte, s *Session, err error) {
	if i.step != 1 {
		return nil, nil, nil, ErrHandshakeOrder
	}
	if reply, _, _, err = i.hs.ReadMessage(nil, msg2); err != nil {
		return nil, nil, nil, err
	}
	msg3, send, recv, err := i.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, nil, nil, err
	}
	i.step = 2
	return reply, msg3, &Session{send: send, recv: recv}, nil
}

// Session encrypts and decrypts transport messages. Messages must be
// decrypted in the order they were sent; the relay keeps that order.
type Session struct {
	smu, rmu sync.Mutex
	send     *noise.CipherState
	recv     *noise.CipherState
}

func (s *Session) Encrypt(plain []byte) ([]byte, error) {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.send.Encrypt(nil, nil, plain)
}

func (s *Session) Decrypt(msg []byte) ([]byte, error) {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return s.recv.Decrypt(nil, nil, msg)
}
