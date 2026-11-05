package server

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A passkey (Face ID, Touch ID, Windows Hello…) guards sensitive apps such as
// Pocket AI: the device key alone is not enough to open them. The browser
// makes the WebAuthn calls; Core checks the results here.

const (
	wanFlagUP = 0x01 // user present
	wanFlagUV = 0x04 // user verified (biometrics or device PIN)
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64u(s string) []byte {
	s = strings.TrimRight(s, "=")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, _ = base64.RawStdEncoding.DecodeString(s)
	}
	return b
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// passkey runs the passkey step: set one up on first use, else verify it.
func (c *remoteChan) passkey(ctx context.Context, acc *remoteAccess) error {
	s := c.h.s
	challenge := randBytes(32)
	k := acc.Key
	if !k.HasPasskey {
		c.ctl(map[string]any{"t": "passkey-setup", "challenge": b64u(challenge), "rp": c.host,
			"app":  acc.App.Name,
			"user": map[string]string{"id": b64u([]byte(c.host + "/" + strconv.FormatInt(k.ID, 10))), "name": acc.User.Email, "display": acc.User.Name + " · " + acc.Device.Name}})
	} else {
		c.ctl(map[string]any{"t": "passkey", "challenge": b64u(challenge), "rp": c.host, "app": acc.App.Name, "id": k.PasskeyID})
	}
	m, err := c.nextCtl(3 * time.Minute)
	if err != nil {
		return errors.New("No passkey answer arrived.")
	}
	str := func(k string) string { v, _ := m[k].(string); return v }
	if str("t") == "passkey-cancel" {
		return errors.New("The passkey check was cancelled.")
	}
	clientData, authData := unb64u(str("clientData")), unb64u(str("authData"))
	if !k.HasPasskey {
		if str("t") != "passkey-setup" {
			return errors.New("Expected a new passkey.")
		}
		if err := checkClientData(clientData, "webauthn.create", challenge, c.host); err != nil {
			return err
		}
		if err := checkAuthData(authData, c.host); err != nil {
			return err
		}
		pub := unb64u(str("publicKey"))
		alg, _ := m["alg"].(float64)
		if _, err := x509.ParsePKIXPublicKey(pub); err != nil || pub == nil {
			return errors.New("This browser did not hand over the passkey's public key.")
		}
		if str("id") == "" {
			return errors.New("The passkey has no id.")
		}
		if err := s.store.SetRemotePasskey(ctx, k.ID, str("id"), b64u(pub), int(alg)); err != nil {
			return err
		}
		k.PasskeyID, k.PasskeyPublic, k.PasskeyAlg, k.HasPasskey = str("id"), b64u(pub), int(alg), true
		s.log.Info("remote: passkey set up", "device", k.DeviceID, "app", k.App)
		return nil
	}
	if str("t") != "passkey" || str("id") != k.PasskeyID {
		return errors.New("That is not this device's passkey.")
	}
	if err := checkClientData(clientData, "webauthn.get", challenge, c.host); err != nil {
		return err
	}
	if err := checkAuthData(authData, c.host); err != nil {
		return err
	}
	digest := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), digest[:]...)
	if err := verifyPasskeySig(unb64u(k.PasskeyPublic), signed, unb64u(str("signature"))); err != nil {
		return errors.New("The passkey signature did not verify.")
	}
	return nil
}

func checkClientData(raw []byte, typ string, challenge []byte, host string) error {
	var cd struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
		Origin    string `json:"origin"`
	}
	if json.Unmarshal(raw, &cd) != nil {
		return errors.New("The passkey answer was malformed.")
	}
	if cd.Type != typ || !bytes.Equal(unb64u(cd.Challenge), challenge) {
		return errors.New("The passkey answer was for another request.")
	}
	o, err := url.Parse(cd.Origin)
	if err != nil || !strings.EqualFold(o.Hostname(), host) || !(o.Scheme == "https" || (o.Scheme == "http" && isLocalhost(host))) {
		return errors.New("The passkey answer came from another address.")
	}
	return nil
}

func isLocalhost(host string) bool { return host == "localhost" || strings.HasSuffix(host, ".localhost") }

func checkAuthData(ad []byte, host string) error {
	if len(ad) < 37 {
		return errors.New("The passkey answer was malformed.")
	}
	rp := sha256.Sum256([]byte(strings.ToLower(host)))
	if !bytes.Equal(ad[:32], rp[:]) {
		return errors.New("The passkey belongs to another address.")
	}
	if ad[32]&wanFlagUP == 0 || ad[32]&wanFlagUV == 0 {
		return errors.New("The passkey check needs Face ID, Touch ID or the device PIN.")
	}
	return nil
}

func verifyPasskeySig(spki, signed, sig []byte) error {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(signed)
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if ecdsa.VerifyASN1(k, digest[:], sig) {
			return nil
		}
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig)
	case ed25519.PublicKey:
		if ed25519.Verify(k, signed, sig) {
			return nil
		}
	}
	return errors.New("bad signature")
}
