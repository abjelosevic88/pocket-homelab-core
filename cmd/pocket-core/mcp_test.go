package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
)

func TestMCPSetup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	var out bytes.Buffer
	args := []string{"setup", "-dir", dir, "-apps", "notes,tasks", "-name", "my-client", "-ttl", "24h"}
	if err := mcpCommand(args, &out); err != nil {
		t.Fatal(err)
	}
	clients, err := config.ReadMCPClients(filepath.Join(dir, "server", "clients.json"))
	if err != nil || len(clients) != 1 {
		t.Fatalf("clients=%v err=%v", clients, err)
	}
	env, err := os.ReadFile(filepath.Join(dir, "client.env"))
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(strings.TrimPrefix(string(env), "export POCKET_MCP_TOKEN="))
	hash := sha256.Sum256([]byte(token))
	if len(token) < 40 || clients[0].TokenHash != hex.EncodeToString(hash[:]) || clients[0].ID != "my-client" || len(clients[0].Apps) != 2 {
		t.Fatal("generated credential mismatch")
	}
	if time.Until(clients[0].ExpiresAt) < 23*time.Hour {
		t.Fatal("expiry mismatch")
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("token printed to terminal")
	}
	for _, name := range []string{"client.env", "server/clients.json", "codex.toml"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("%s mode=%v", name, st.Mode())
		}
	}
	if err := mcpCommand(args, &out); err == nil {
		t.Fatal("overwrote existing credentials")
	}
	again, _ := os.ReadFile(filepath.Join(dir, "client.env"))
	if !bytes.Equal(env, again) {
		t.Fatal("existing credentials changed")
	}
}

func TestMCPRejectsUnsafeClientEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://remote.test/mcp", "https://user:secret@example.com/mcp", "https://example.com/mcp?token=x", "file:///etc/passwd"} {
		if err := validateMCPEndpoint(endpoint); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:8080/mcp", "https://core.example.com/mcp", "http://[::1]:8080/mcp"} {
		if err := validateMCPEndpoint(endpoint); err != nil {
			t.Errorf("rejected %s: %v", endpoint, err)
		}
	}
}
