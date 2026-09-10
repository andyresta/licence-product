// Package config loads licence-product's runtime configuration from environment
// variables — there is no install wizard here (unlike FixUnit): this server is
// deployed once by the vendor, not per-customer, so plain env vars are enough.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	AppPort        string
	DatabaseURL    string // full Postgres DSN, e.g. postgres://user:pass@host:5432/dbname?sslmode=disable
	SigningSeedHex string // 64 hex chars = 32-byte Ed25519 seed; see internal/crypto
	SessionSecret  string // HMAC key for signing admin session cookies
	BootstrapAdmin string // "username:password" — created on first startup if admin_users is empty; empty disables bootstrap
}

func Load() (*Config, error) {
	cfg := &Config{
		AppPort:        getEnv("APP_PORT", "8090"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		SigningSeedHex: os.Getenv("LICENSE_SIGNING_SEED"),
		SessionSecret:  os.Getenv("SESSION_SECRET"),
		BootstrapAdmin: os.Getenv("BOOTSTRAP_ADMIN"),
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.SigningSeedHex == "" {
		return nil, fmt.Errorf("config: LICENSE_SIGNING_SEED is required (64 hex chars — see README's \"Generating keys\" section)")
	}
	if cfg.SessionSecret == "" {
		return nil, fmt.Errorf("config: SESSION_SECRET is required")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
