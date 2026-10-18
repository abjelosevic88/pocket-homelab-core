package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestPKCE(t *testing.T) {
	v := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(v))
	ch := base64.RawURLEncoding.EncodeToString(sum[:])
	if !PKCEOK(ch, "S256", v) {
		t.Fatal("S256 should match")
	}
	if PKCEOK(ch, "S256", v+"x") {
		t.Fatal("wrong verifier matched")
	}
	if !PKCEOK("", "", "") {
		t.Fatal("no challenge means no check")
	}
}
