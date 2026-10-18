// Package crypto holds the small primitives Core needs: random tokens, token
// hashing, and AES-GCM for the credentials it keeps on behalf of paired
// devices. The AES key lives in DATA_DIR/secret.key and is generated once.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Token returns a random URL-safe token with the given prefix, e.g. "pca_…".
func Token(prefix string, bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the hex SHA-256 of a token; tokens are stored hashed.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Prefix returns the first n characters of a token for display.
func Prefix(token string, n int) string {
	if len(token) <= n {
		return token
	}
	return token[:n]
}

// Code returns a human-typable code of n characters from an unambiguous alphabet.
func Code(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// Box encrypts and decrypts strings with a key loaded from (or written to) a file.
type Box struct {
	aead cipher.AEAD
}

// LoadBox reads the 32-byte key at path, creating it (0600) when missing.
func LoadBox(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("write secret key: %w", err)
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key at %s must be exactly 32 bytes (got %d)", path, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext; the result is "enc:" + base64(nonce|ciphertext).
// Empty strings stay empty so blanks stay recognisable in the database.
func (b *Box) Seal(plain string) string {
	if plain == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	ct := b.aead.Seal(nil, nonce, []byte(plain), nil)
	return "enc:" + base64.RawStdEncoding.EncodeToString(append(nonce, ct...))
}

// Open decrypts a value produced by Seal. Values without the prefix are
// returned as-is so plain rows imported by hand keep working.
func (b *Box) Open(v string) (string, error) {
	if len(v) < 4 || v[:4] != "enc:" {
		return v, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(v[4:])
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
