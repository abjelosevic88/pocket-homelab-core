// Package oidc is a small OpenID Connect provider: RS256 ID tokens, the
// authorization-code flow with PKCE, discovery, JWKS and userinfo. It is
// enough for Pocket apps and the common self-hosted apps (Immich,
// Nextcloud, Paperless, Jellyfin's SSO plugin) to log in with one account.
package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/store"
)

// Provider signs tokens with one RSA key kept in the settings table.
type Provider struct {
	Issuer string
	key    *rsa.PrivateKey
	kid    string
}

// New loads or creates the signing key.
func New(ctx context.Context, st *store.Store, issuer string) (*Provider, error) {
	p := &Provider{Issuer: strings.TrimRight(issuer, "/")}
	if pemStr, ok := st.KVGet(ctx, "oidc:key"); ok && pemStr != "" {
		block, _ := pem.Decode([]byte(pemStr))
		if block == nil {
			return nil, errors.New("oidc key: bad PEM")
		}
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("oidc key: %w", err)
		}
		p.key = k
	} else {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
		if err := st.KVPut(ctx, "oidc:key", pemStr); err != nil {
			return nil, err
		}
		p.key = k
	}
	sum := sha256.Sum256(p.key.PublicKey.N.Bytes())
	p.kid = base64.RawURLEncoding.EncodeToString(sum[:8])
	return p, nil
}

// Discovery is the openid-configuration document.
func (p *Provider) Discovery() map[string]any {
	return map[string]any{
		"issuer":                                p.Issuer,
		"authorization_endpoint":                p.Issuer + "/oidc/authorize",
		"token_endpoint":                        p.Issuer + "/oidc/token",
		"userinfo_endpoint":                     p.Issuer + "/oidc/userinfo",
		"jwks_uri":                              p.Issuer + "/oidc/jwks",
		"end_session_endpoint":                  p.Issuer + "/oidc/logout",
		"introspection_endpoint":                p.Issuer + "/api/v1/auth/introspect",
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups", "offline_access"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"claims_supported":                      []string{"sub", "iss", "aud", "exp", "iat", "nonce", "email", "email_verified", "name", "preferred_username", "groups"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
	}
}

// JWKS publishes the public key.
func (p *Provider) JWKS() map[string]any {
	pub := p.key.PublicKey
	return map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": p.kid,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
}

// Claims builds the standard claim set for a user.
func Claims(u *store.User) map[string]any {
	groups := []string{"users"}
	if u.IsAdmin() {
		groups = append(groups, "admin")
	}
	name := u.Name
	if name == "" {
		name = u.Email
	}
	username := u.Email
	if i := strings.Index(username, "@"); i > 0 {
		username = username[:i]
	}
	return map[string]any{
		"sub": fmt.Sprint(u.ID), "email": u.Email, "email_verified": true,
		"name": name, "preferred_username": username, "groups": groups,
	}
}

// IDToken signs an ID token for the user and client. extra adds claims
// that depend on the client (pocket_uid); it never overrides the standard ones.
func (p *Provider) IDToken(u *store.User, clientID, nonce string, ttl time.Duration, extra map[string]any) (string, error) {
	now := time.Now()
	claims := Claims(u)
	for k, v := range extra {
		if _, std := claims[k]; !std {
			claims[k] = v
		}
	}
	claims["iss"] = p.Issuer
	claims["aud"] = clientID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(ttl).Unix()
	claims["auth_time"] = now.Unix()
	if nonce != "" {
		claims["nonce"] = nonce
	}
	return p.sign(claims)
}

func (p *Provider) sign(claims map[string]any) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": p.kid})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks a token this provider signed and returns its claims.
func (p *Provider) Verify(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&p.key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("bad signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, err
	}
	if exp, ok := claims["exp"].(float64); ok && time.Now().Unix() > int64(exp) {
		return nil, errors.New("expired")
	}
	return claims, nil
}

// PKCEOK checks a verifier against the stored challenge (S256 or plain).
func PKCEOK(challenge, method, verifier string) bool {
	if challenge == "" {
		return true
	}
	if verifier == "" {
		return false
	}
	switch strings.ToUpper(method) {
	case "", "S256":
		sum := sha256.Sum256([]byte(verifier))
		return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
	case "PLAIN":
		return verifier == challenge
	}
	return false
}
