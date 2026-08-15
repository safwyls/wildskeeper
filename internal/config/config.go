package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Config holds all runtime configuration, sourced entirely from environment
// variables so the container can be configured without a config file.
type Config struct {
	// HTTPAddr is the address the HTTP server listens on, e.g. ":8080".
	HTTPAddr string

	// DataDir is where the sqlite database lives. Mount this as a volume.
	DataDir string

	// JWTSecret signs session cookies. Must stay stable across restarts or
	// existing sessions are invalidated.
	JWTSecret []byte

	// EncryptionKey encrypts stored RCON/REST passwords at rest. Must be
	// exactly 32 bytes. Losing this key makes stored server credentials
	// unrecoverable, so back it up alongside the database.
	EncryptionKey []byte

	// Bootstrap admin, only used the first time the app starts (when the
	// users table is empty).
	AdminUsername string
	AdminPassword string

	// DockerHost points at a scoped docker socket proxy used to start and
	// stop game server containers. Empty disables power control entirely —
	// Wildskeeper should never require access to a docker socket to run.
	DockerHost string

	// IlmariURL/Token point at the shared Ilmari host service. When set,
	// it wins over ProvisionerURL — the cut-over flag of the Ilmari
	// migration (docs/migration.md in the ilmari repo). Unset it and the
	// console falls straight back to the legacy provisioner below.
	IlmariURL   string
	IlmariToken string
	// ProvisionerURL/Token point at a provisioner-mode wkagent — the one
	// component allowed to create containers. Empty means the new-server
	// wizard hands the operator a stack file to paste instead.
	ProvisionerURL   string
	ProvisionerToken string

	// CookieSecure marks the session cookie Secure for deployments behind
	// TLS. Off by default so plain-HTTP LAN setups keep working.
	CookieSecure bool

	// AnthropicAPIKey / GeminiAPIKey enable the pal advisor chat (hosted-
	// model analysis of pals and base crews) — set one or the other. Both
	// empty disables the feature entirely: like DockerHost, absent means
	// the UI never offers it, not that it breaks. If both are set,
	// Anthropic wins (see cmd/wildskeeper).
	AnthropicAPIKey string
	GeminiAPIKey    string
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "wildskeeper.db")
}

// Load reads configuration from the environment. Required variables are
// JWT_SECRET and ENCRYPTION_KEY; everything else has a sane default for
// local development.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:      getEnv("HTTP_ADDR", ":8080"),
		DataDir:       getEnv("DATA_DIR", "./data"),
		AdminUsername: getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		DockerHost:    os.Getenv("DOCKER_HOST"),
		// The phase 5 provisioner (docs/sidecar-agent.md): when set, the
		// new-server wizard deploys stacks itself instead of handing the
		// operator a file to paste.
		IlmariURL:        os.Getenv("ILMARI_URL"),
		IlmariToken:      os.Getenv("ILMARI_TOKEN"),
		ProvisionerURL:   os.Getenv("PROVISIONER_URL"),
		ProvisionerToken: os.Getenv("PROVISIONER_TOKEN"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
	}

	cfg.CookieSecure = os.Getenv("COOKIE_SECURE") == "true" || os.Getenv("COOKIE_SECURE") == "1"

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	// A short secret makes session forgery brute-forceable; .env.example
	// already tells people to generate 32+ chars, so enforce it.
	if len(jwtSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters, got %d (generate one with `openssl rand -hex 32`)", len(jwtSecret))
	}
	cfg.JWTSecret = []byte(jwtSecret)

	encKey := os.Getenv("ENCRYPTION_KEY")
	if len(encKey) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY is required and must be exactly 32 bytes, got %d", len(encKey))
	}
	cfg.EncryptionKey = []byte(encKey)

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	return cfg, nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
