// Package config loads runtime configuration from environment variables.
//
// Every option has a sensible default so that `docker run` with a single
// volume works out of the box. See docs/configuration.md for the full list.
package config

import (
	"fmt"
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

	DemoData bool
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

		DemoData: envBool("DEMO_DATA", false),
	}
	if c.BaseURL == "" {
		c.BaseURL = fmt.Sprintf("http://localhost:%d", c.Port)
	}
	if c.OIDCIssuer == "" {
		c.OIDCIssuer = c.BaseURL
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
