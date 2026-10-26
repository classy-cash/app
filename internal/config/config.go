// Package config loads the explicit, versioned environment configuration.
//
// Every variable is documented in docs/INTEGRATION.md. CLASSYCASH_CONFIG_VERSION must match
// version.ConfigVersion when set, so an operator notices an incompatible configuration at start-up
// instead of at runtime.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/classy-cash/app/internal/version"
)

// Config is the complete runtime configuration.
type Config struct {
	Env        string // "prod" or "dev"
	Addr       string
	BaseURL    string
	DBPath     string
	DataDir    string
	InstanceID string
	Location   *time.Location
	MemLimitMB int

	SessionSecret string // HMAC key for CSRF tokens; required in prod
	ClaimToken    string // one-time owner claim for first-run onboarding

	Mail MailConfig
	Bank BankConfig

	Demo      bool
	LogLevel  string
	TrustProx bool
}

// MailConfig selects how outgoing mail is delivered.
type MailConfig struct {
	Driver      string // relay | log | capture
	RelayURL    string
	RelayKey    string
	FromName    string
	FromAddress string // informational: the relay owns the visible From address
}

// BankConfig configures the signed bank-mail webhook.
type BankConfig struct {
	Address     string // the random inbound address handed to the bank (display only)
	Secret      string // HMAC shared secret with the Email Worker (>= 32 chars)
	FromAddress string // optional extra check: exact expected mail_from (the Worker already enforces it)
	MaxBody     int64
}

// Enabled reports whether the webhook may accept deliveries.
func (b BankConfig) Enabled() bool { return len(b.Secret) >= 32 }

// Load reads configuration from the environment (getenv is injectable for tests).
func Load(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv("CLASSYCASH_" + k)); v != "" {
			return v
		}
		return def
	}
	if v := get("CONFIG_VERSION", ""); v != "" && v != strconv.Itoa(version.ConfigVersion) {
		return nil, fmt.Errorf("CLASSYCASH_CONFIG_VERSION=%s, this build understands %d (see docs/INTEGRATION.md)", v, version.ConfigVersion)
	}
	c := &Config{
		Env:        get("ENV", "prod"),
		Addr:       get("ADDR", "127.0.0.1:8080"),
		BaseURL:    strings.TrimRight(get("BASE_URL", "http://localhost:8080"), "/"),
		DBPath:     get("DB_PATH", "data/classycash.db"),
		DataDir:    get("DATA_DIR", "data"),
		InstanceID: get("INSTANCE_ID", "local"),
		LogLevel:   get("LOG_LEVEL", "info"),
		Demo:       truthy(get("DEMO_MODE", "")),
		TrustProx:  truthy(get("TRUST_PROXY", "")),
		ClaimToken: get("CLAIM_TOKEN", ""),
	}
	c.SessionSecret = get("SESSION_SECRET", "")
	var err error
	if c.MemLimitMB, err = atoi(get("MEMORY_LIMIT_MB", "160")); err != nil {
		return nil, fmt.Errorf("CLASSYCASH_MEMORY_LIMIT_MB: %w", err)
	}
	if c.Location, err = time.LoadLocation(get("TIMEZONE", "Europe/Warsaw")); err != nil {
		return nil, fmt.Errorf("CLASSYCASH_TIMEZONE: %w", err)
	}
	c.Mail = MailConfig{
		Driver:      get("MAIL_DRIVER", ""),
		RelayURL:    get("MAIL_RELAY_URL", ""),
		RelayKey:    get("MAIL_RELAY_KEY", ""),
		FromName:    get("MAIL_FROM_NAME", "Classy Cash"),
		FromAddress: get("MAIL_FROM_ADDRESS", "no-reply@classycash.dpdns.org"),
	}
	c.Bank = BankConfig{
		Address:     strings.ToLower(get("BANK_MAIL_ADDRESS", "")),
		Secret:      get("BANK_MAIL_SECRET", ""),
		FromAddress: strings.ToLower(get("BANK_MAIL_FROM", "")),
		MaxBody:     128 << 10,
	}
	return c, c.normalize()
}

func (c *Config) normalize() error {
	if c.Env != "prod" && c.Env != "dev" {
		return fmt.Errorf("CLASSYCASH_ENV must be prod or dev, got %q", c.Env)
	}
	if _, err := url.Parse(c.BaseURL); err != nil {
		return fmt.Errorf("CLASSYCASH_BASE_URL: %w", err)
	}
	if c.Demo {
		// Trusted runtime configuration: a demo can never reach real mail or the bank webhook.
		c.Mail.Driver = "capture"
		c.Mail.RelayURL, c.Mail.RelayKey = "", ""
		c.Bank.Secret, c.Bank.Address = "", ""
	}
	if c.Mail.Driver == "" {
		switch {
		case c.Mail.RelayURL != "":
			c.Mail.Driver = "relay"
		case c.Env == "dev":
			c.Mail.Driver = "log"
		default:
			c.Mail.Driver = "capture"
		}
	}
	switch c.Mail.Driver {
	case "relay":
		if c.Mail.RelayURL == "" || c.Mail.RelayKey == "" {
			return errors.New("MAIL_DRIVER=relay needs CLASSYCASH_MAIL_RELAY_URL and CLASSYCASH_MAIL_RELAY_KEY")
		}
		if u, err := url.Parse(c.Mail.RelayURL); err != nil || (u.Scheme != "https" && c.Env == "prod") {
			return errors.New("CLASSYCASH_MAIL_RELAY_URL must be an https URL in prod")
		}
	case "log", "capture":
	default:
		return fmt.Errorf("unknown CLASSYCASH_MAIL_DRIVER %q", c.Mail.Driver)
	}
	if c.Env == "prod" && len(c.SessionSecret) < 32 {
		return errors.New("CLASSYCASH_SESSION_SECRET (>= 32 chars) is required in prod")
	}
	if c.Bank.Secret != "" && len(c.Bank.Secret) < 32 {
		return errors.New("CLASSYCASH_BANK_MAIL_SECRET must be at least 32 characters")
	}
	if c.SessionSecret == "" {
		c.SessionSecret = "dev-only-session-secret-not-for-production"
	}
	return nil
}

// Secure reports whether cookies must carry the Secure attribute.
func (c *Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func atoi(s string) (int, error) { return strconv.Atoi(s) }
