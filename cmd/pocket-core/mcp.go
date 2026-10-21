package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-core/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runMCP(args []string) {
	if err := mcpCommand(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		os.Exit(1)
	}
}

func mcpCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pocket-core mcp setup|check (use -h for options)")
	}
	switch args[0] {
	case "setup":
		fs := flag.NewFlagSet("mcp setup", flag.ContinueOnError)
		fs.SetOutput(out)
		dir := fs.String("dir", "./mcp-secrets", "new private directory for generated configuration; must not exist")
		apps := fs.String("apps", "notes", "comma-separated app slugs, or * for all registered apps")
		name := fs.String("name", "codex", "client identifier for audit logs")
		endpoint := fs.String("url", "http://127.0.0.1:8080/mcp", "MCP endpoint for the client")
		ttl := fs.Duration("ttl", 90*24*time.Hour, "credential lifetime")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected setup arguments")
		}
		if err := validateMCPEndpoint(*endpoint); err != nil {
			return err
		}
		if *ttl <= 0 {
			return errors.New("ttl must be positive")
		}
		appList := strings.Split(*apps, ",")
		for i := range appList {
			appList[i] = strings.TrimSpace(appList[i])
		}
		if err := config.ValidateMCPApps(appList); err != nil {
			return err
		}
		// Use the same identifier grammar as app slugs, excluding the wildcard.
		if *name == "*" {
			return errors.New("invalid client name")
		}
		if err := config.ValidateMCPApps([]string{*name}); err != nil {
			return err
		}
		privateDir, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		if err := os.Mkdir(privateDir, 0700); err != nil {
			return fmt.Errorf("create new configuration directory: %w", err)
		}
		success := false
		defer func() {
			if !success {
				_ = os.RemoveAll(privateDir)
			}
		}()
		if err := os.Mkdir(filepath.Join(privateDir, "server"), 0700); err != nil {
			return err
		}
		token := "pcm_" + rand.Text() + rand.Text()
		hash := sha256.Sum256([]byte(token))
		client := config.MCPClient{ID: *name, TokenHash: hex.EncodeToString(hash[:]), Apps: appList, ExpiresAt: time.Now().UTC().Add(*ttl)}
		encoded, err := json.MarshalIndent([]config.MCPClient{client}, "", "  ")
		if err != nil {
			return err
		}
		files := map[string]string{
			"server/clients.json": string(encoded) + "\n",
			"client.env":          "export POCKET_MCP_TOKEN=" + token + "\n",
			"codex.toml":          "[mcp_servers.pocket_status]\nurl = " + strconv.Quote(*endpoint) + "\nbearer_token_env_var = \"POCKET_MCP_TOKEN\"\n",
		}
		for name, contents := range files {
			if err := os.WriteFile(filepath.Join(privateDir, name), []byte(contents), 0600); err != nil {
				return err
			}
		}
		success = true
		fmt.Fprintf(out, "Created private MCP configuration in %s\nClient: %s; apps: %s; expires: %s\nSet Core's MCP_CLIENTS_FILE to %s\nLoad client.env in the client shell and copy codex.toml into your Codex configuration.\nKeep client.env private; it contains the token. Core only needs server/clients.json.\n", privateDir, client.ID, strings.Join(client.Apps, ","), client.ExpiresAt.Format(time.RFC3339), filepath.Join(privateDir, "server", "clients.json"))
		return nil
	case "check":
		fs := flag.NewFlagSet("mcp check", flag.ContinueOnError)
		fs.SetOutput(out)
		endpoint := fs.String("url", "http://127.0.0.1:8080/mcp", "MCP endpoint")
		app := fs.String("app", "", "app slug for a fresh probe; omit to list apps")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected check arguments")
		}
		if err := validateMCPEndpoint(*endpoint); err != nil {
			return err
		}
		token := os.Getenv("POCKET_MCP_TOKEN")
		if token == "" {
			return errors.New("set POCKET_MCP_TOKEN in the client environment")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		client := mcp.NewClient(&mcp.Implementation{Name: "pocket-core-check", Version: version}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint,
			HTTPClient: &http.Client{Transport: mcpBearerTransport{token}, Timeout: 15 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
			DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			return fmt.Errorf("connect MCP: %w", err)
		}
		defer session.Close()
		params := &mcp.CallToolParams{Name: "pocket_list_apps", Arguments: map[string]any{}}
		if *app != "" {
			params.Name = "pocket_app_status"
			params.Arguments = map[string]any{"slug": *app, "fresh": true}
		}
		result, err := session.CallTool(ctx, params)
		if err != nil {
			return fmt.Errorf("call MCP: %w", err)
		}
		if result.IsError {
			return errors.New("MCP tool rejected the request; check the app slug and client permissions")
		}
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result.StructuredContent)
	default:
		return errors.New("usage: pocket-core mcp setup|check")
	}
}

func validateMCPEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("MCP URL must have a host and no credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return nil
	}
	return errors.New("use HTTPS for remote MCP; HTTP is allowed only on loopback")
}

type mcpBearerTransport struct{ token string }

func (t mcpBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
