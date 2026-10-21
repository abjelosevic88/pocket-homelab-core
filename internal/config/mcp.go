package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

// MCPClient grants only operational status access, independent of household identities.
type MCPClient struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	Apps      []string  `json:"apps"`
	ExpiresAt time.Time `json:"expires_at"`
}

var mcpSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func ValidateMCPApps(apps []string) error {
	if len(apps) == 0 {
		return errors.New("MCP apps must explicitly list app slugs (or * for all registered apps)")
	}
	for _, app := range apps {
		if app != "*" && !mcpSlug.MatchString(app) {
			return fmt.Errorf("invalid MCP app slug %q", app)
		}
	}
	return nil
}

// ReadMCPClients deliberately reloads a small file on every authenticated request.
// Atomic replacement applies revocation and permission edits without restarting Core.
func ReadMCPClients(path string) ([]MCPClient, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 64<<10 {
		return nil, errors.New("MCP client file exceeds 64 KiB")
	}
	var clients []MCPClient
	if err := json.Unmarshal(b, &clients); err != nil {
		return nil, errors.New("invalid MCP client JSON")
	}
	ids, hashes := map[string]bool{}, map[string]bool{}
	for _, c := range clients {
		hash, err := hex.DecodeString(c.TokenHash)
		if !mcpSlug.MatchString(c.ID) || ids[c.ID] {
			return nil, errors.New("invalid or duplicate MCP client ID")
		}
		if err != nil || len(hash) != sha256.Size || hashes[string(hash)] {
			return nil, errors.New("invalid or duplicate MCP token hash")
		}
		if c.ExpiresAt.IsZero() {
			return nil, errors.New("MCP client expiry is required")
		}
		if err := ValidateMCPApps(c.Apps); err != nil {
			return nil, err
		}
		ids[c.ID], hashes[string(hash)] = true, true
	}
	return clients, nil
}
