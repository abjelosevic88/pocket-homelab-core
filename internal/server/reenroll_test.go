package server

import (
	"testing"

	"github.com/abjelosevic88/pocket-homelab-core/internal/crypto"
	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// An app that registers with the registration key again (its first answer was lost) gets a new
// token and a new client secret; one that registers with its token gets neither.
func TestReenrollMintsNewClientSecret(t *testing.T) {
	e := newEnv(t)
	if c := e.do("POST", "/api/v1/auth/setup", "", map[string]string{"email": "a@b.c", "password": "password123", "name": "A"}, nil); c != 200 {
		t.Fatalf("setup %d", c)
	}
	var settings store.Settings
	e.do("GET", "/api/v1/settings", "", nil, &settings)
	type answer struct {
		Token string `json:"token"`
		OIDC  struct {
			ClientSecret string `json:"client_secret"`
		} `json:"oidc"`
	}
	m := map[string]any{"slug": "photos", "name": "Pocket Photos", "url": e.srv.URL, "version": "1.0",
		"oidc": map[string]any{"redirect_uris": []string{"http://rp/cb"}}}
	var first, again, byToken answer
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, &first); c != 201 || first.Token == "" || first.OIDC.ClientSecret == "" {
		t.Fatalf("first registration %d %+v", c, first)
	}
	if c := e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, m, &again); c != 200 || again.Token == "" || again.OIDC.ClientSecret == "" {
		t.Fatalf("registration with the key again %d %+v", c, again)
	}
	if again.Token == first.Token || again.OIDC.ClientSecret == first.OIDC.ClientSecret {
		t.Fatalf("the key again must mint a new token and a new secret")
	}
	cl, err := e.st.GetOIDCClient(t.Context(), "pocket-photos")
	if err != nil || cl.SecretHash != crypto.Hash(again.OIDC.ClientSecret) {
		t.Fatalf("stored secret must be the new one (err %v)", err)
	}
	if c := e.do("POST", "/api/v1/apps/register", again.Token, m, &byToken); c != 200 || byToken.Token != "" || byToken.OIDC.ClientSecret != "" {
		t.Fatalf("registration with the token must mint nothing: %d %+v", c, byToken)
	}
	// A public client (PKCE only) never gets a secret.
	pub := map[string]any{"slug": "pub", "name": "Pub", "url": e.srv.URL, "oidc": map[string]any{"redirect_uris": []string{"http://rp/cb"}, "public": true}}
	var p1, p2 answer
	e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, pub, &p1)
	e.do("POST", "/api/v1/apps/register", settings.RegistrationKey, pub, &p2)
	if p1.OIDC.ClientSecret != "" || p2.OIDC.ClientSecret != "" {
		t.Fatalf("public client got a secret: %+v %+v", p1, p2)
	}
}
