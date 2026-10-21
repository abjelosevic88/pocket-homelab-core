// Package config loads runtime configuration from environment variables.
//
// Every option has a sensible default so that `docker run` with a single
// volume works out of the box. See docs/configuration.md for the full list.
package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings.
type Config struct {
	Host       string
	Port       int
	BaseURL    string // public URL of Core itself (OIDC issuer, pairing links)
	DataDir    string
	DBPath     string
	SecretPath string
	LogLevel   string
	LogFormat  string // text | json
	TrustProxy bool

	AuthMode             string // local | proxy | none
	AuthProxyHeader      string
	AuthProxyEmailHeader string
	SessionTTL           time.Duration
	SecureCookies        bool
	AdminEmail           string
	AdminPassword        string
	AdminName            string

	// RegistrationKey lets a Pocket app register itself the first time. When
	// empty, one is generated on first start and shown under Settings.
	RegistrationKey string

	PairTTL         time.Duration // how long a pairing code is valid
	HealthInterval  time.Duration // how often registered apps are polled
	EventRetention  time.Duration // events older than this are purged
	WebhookAttempts int

	OIDCIssuer string // defaults to BaseURL

	// Signing in to Core itself through an upstream provider (PocketID…).
	SSOIssuer       string
	SSOClientID     string
	SSOClientSecret string
	SSOName         string
	SSOAutoCreate   bool
	PasswordLogin   bool // false = only the upstream provider; tokens keep working

	DemoData bool

	// MCP is opt-in and has a separate status-only credential.
	MCPTokenHash       string
	MCPApps            []string
	MCPClientsFile     string
	MCPOAuthEnabled    bool
	MCPAppToolsEnabled bool
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(env(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envInt(key string, def int) int {
	if v := env(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := env(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	dataDir := env("DATA_DIR", "./data")
	c := &Config{
		Host:       env("HOST", "0.0.0.0"),
		Port:       envInt("PORT", 8080),
		BaseURL:    strings.TrimRight(env("BASE_URL", ""), "/"),
		DataDir:    dataDir,
		DBPath:     env("DB_PATH", dataDir+"/pocket-core.db"),
		SecretPath: env("SECRET_PATH", dataDir+"/secret.key"),
		LogLevel:   strings.ToLower(env("LOG_LEVEL", "info")),
		LogFormat:  strings.ToLower(env("LOG_FORMAT", "text")),
		TrustProxy: envBool("TRUST_PROXY", true),

		AuthMode:             strings.ToLower(env("AUTH_MODE", "local")),
		AuthProxyHeader:      env("AUTH_PROXY_HEADER", "Remote-User"),
		AuthProxyEmailHeader: env("AUTH_PROXY_EMAIL_HEADER", "Remote-Email"),
		SessionTTL:           envDuration("SESSION_TTL", 720*time.Hour),
		SecureCookies:        envBool("SECURE_COOKIES", false),
		AdminEmail:           strings.ToLower(env("ADMIN_EMAIL", "")),
		AdminPassword:        env("ADMIN_PASSWORD", ""),
		AdminName:            env("ADMIN_NAME", "Admin"),

		RegistrationKey: env("REGISTRATION_KEY", ""),

		PairTTL:         envDuration("PAIR_TTL", 10*time.Minute),
		HealthInterval:  envDuration("HEALTH_INTERVAL", 60*time.Second),
		EventRetention:  envDuration("EVENT_RETENTION", 30*24*time.Hour),
		WebhookAttempts: envInt("WEBHOOK_ATTEMPTS", 8),

		OIDCIssuer: strings.TrimRight(env("OIDC_ISSUER", ""), "/"),

		SSOIssuer:       strings.TrimRight(env("SSO_ISSUER", ""), "/"),
		SSOClientID:     env("SSO_CLIENT_ID", ""),
		SSOClientSecret: env("SSO_CLIENT_SECRET", ""),
		SSOName:         env("SSO_NAME", "single sign-on"),
		SSOAutoCreate:   envBool("SSO_AUTO_CREATE", false),
		PasswordLogin:   envBool("PASSWORD_LOGIN", true),

		DemoData:           envBool("DEMO_DATA", false),
		MCPTokenHash:       env("MCP_TOKEN_HASH", ""),
		MCPOAuthEnabled:    envBool("MCP_OAUTH_ENABLED", false),
		MCPAppToolsEnabled: envBool("MCP_APP_TOOLS_ENABLED", false),
		MCPClientsFile:     env("MCP_CLIENTS_FILE", ""),
		MCPApps:            strings.FieldsFunc(env("MCP_APPS", ""), func(r rune) bool { return r == ',' || r == ' ' }),
	}
	if c.MCPTokenHash != "" {
		b, err := hex.DecodeString(c.MCPTokenHash)
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("MCP_TOKEN_HASH must be a hex SHA-256 digest")
		}
		if err := ValidateMCPApps(c.MCPApps); err != nil {
			return nil, err
		}
	}
	if c.MCPClientsFile != "" {
		if c.MCPTokenHash != "" {
			return nil, fmt.Errorf("use either MCP_CLIENTS_FILE or MCP_TOKEN_HASH, not both")
		}
		if _, err := ReadMCPClients(c.MCPClientsFile); err != nil {
			return nil, fmt.Errorf("MCP_CLIENTS_FILE: %w", err)
		}
	}
	if c.BaseURL == "" {
		c.BaseURL = fmt.Sprintf("http://localhost:%d", c.Port)
	}
	if c.MCPAppToolsEnabled && !c.MCPOAuthEnabled {
		return nil, fmt.Errorf("MCP_APP_TOOLS_ENABLED requires MCP_OAUTH_ENABLED")
	}
	if c.MCPOAuthEnabled {
		u, err := url.Parse(c.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return nil, fmt.Errorf("MCP_OAUTH_ENABLED needs an HTTPS BASE_URL origin (HTTP loopback is allowed for development)")
		}
		if c.AuthMode != "local" {
			return nil, fmt.Errorf("MCP_OAUTH_ENABLED requires AUTH_MODE=local (password or upstream SSO login)")
		}
	}
	if c.OIDCIssuer == "" {
		c.OIDCIssuer = c.BaseURL
	}
	if c.SSOIssuer != "" && c.SSOClientID == "" {
		return nil, fmt.Errorf("SSO_ISSUER needs SSO_CLIENT_ID (the callback is BASE_URL/api/v1/auth/sso/callback)")
	}
	if c.SSOIssuer != "" && c.SSOIssuer == c.OIDCIssuer {
		return nil, fmt.Errorf("SSO_ISSUER is Core's own issuer — point it at the upstream provider")
	}
	if !c.PasswordLogin && c.SSOIssuer == "" {
		c.PasswordLogin = true // never lock everyone out
	}
	switch c.AuthMode {
	case "local", "proxy", "none":
	default:
		return nil, fmt.Errorf("AUTH_MODE must be one of local, proxy, none (got %q)", c.AuthMode)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return nil, fmt.Errorf("PORT out of range: %d", c.Port)
	}
	if c.HealthInterval < 10*time.Second {
		c.HealthInterval = 10 * time.Second
	}
	if c.WebhookAttempts < 1 {
		c.WebhookAttempts = 1
	}
	return c, nil
}

// Addr returns host:port.
func (c *Config) Addr() string { return fmt.Sprintf("%s:%d", c.Host, c.Port) }
