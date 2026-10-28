package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBoxMakesKeyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b, err := LoadBox(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("key: %d bytes, %v", len(key), err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file left behind")
	}
	sealed := b.Seal("hello")
	again, err := LoadBox(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := again.Open(sealed); err != nil || plain != "hello" {
		t.Fatalf("reopened key: %q, %v", plain, err)
	}
}

func TestLoadBoxRefusesEmptyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBox(path); err == nil {
		t.Fatal("an empty key must not load")
	}
}
