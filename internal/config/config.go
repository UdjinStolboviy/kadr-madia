// Package config loads Kadr runtime configuration from environment variables
// (see docs/OPERATIONS.md §4). Configuration is loaded once in main and passed
// explicitly to every component; there is no package-level state.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DevPepper is the API key pepper used when KADR_DEV=1 and no pepper is set.
// It must never be used in production.
const DevPepper = "kadr-dev-pepper-not-for-production-use!"

// MinPepperLen is the minimal accepted length of KADR_API_KEY_PEPPER.
const MinPepperLen = 32

// Config is the fully resolved runtime configuration.
type Config struct {
	PublicAddr      string // /v1, /metrics — LAN only
	AdminAddr       string // /admin + embedded UI
	DBPath          string
	TmpDir          string
	MasterKeyFile   string
	APIKeyPepper    []byte
	PoolImage       int
	PoolVideo       int
	PoolFile        int
	VideoHW         string   // auto | vaapi | off
	TelegramToken   string   // KADR_TELEGRAM_BOT_TOKEN (alerts)
	TelegramChatID  string   // KADR_TELEGRAM_CHAT_ID
	EdgeBaseURL     string   // https://media.example.com — private delivery host (kadr-edge)
	CFAccountID     string   // KADR_CF_ACCOUNT_ID
	CFAPIToken      string   // KADR_CF_API_TOKEN
	CFWorkerName    string   // KADR_CF_WORKER_NAME (default kadr-edge)
	WebAuthnRPID    string   // KADR_WEBAUTHN_RP_ID (default localhost in dev; empty disables passkeys)
	WebAuthnOrigins []string // KADR_WEBAUTHN_ORIGINS, comma separated
	AdminPublicURL  string   // KADR_ADMIN_PUBLIC_URL — external admin URL (MCP endpoint, notification links)
	LogLevel        slog.Level
	Dev             bool
}

// Getenv abstracts os.Getenv for tests.
type Getenv func(string) string

// Load reads configuration from the environment. It applies defaults
// (dev-friendly ones when KADR_DEV=1) but does not validate secrets; call
// Validate before starting anything that needs them.
func Load(getenv Getenv) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	dev := parseBool(getenv("KADR_DEV"))

	c := &Config{Dev: dev}
	if dev {
		c.PublicAddr = "127.0.0.1:8080"
		c.AdminAddr = "127.0.0.1:8081"
		c.DBPath = filepath.Join("var", "kadr.db")
		c.TmpDir = filepath.Join("var", "tmp")
		c.MasterKeyFile = filepath.Join("var", "master.key")
	} else {
		// Safe default: bind loopback unless an explicit LAN address is set.
		c.PublicAddr = "127.0.0.1:8080"
		c.AdminAddr = "127.0.0.1:8081"
		c.DBPath = "/var/lib/kadr/db/kadr.db"
		c.TmpDir = "/var/lib/kadr/tmp"
		c.MasterKeyFile = "/etc/kadr/master.key"
	}

	setStr(&c.PublicAddr, getenv("KADR_PUBLIC_ADDR"))
	setStr(&c.AdminAddr, getenv("KADR_ADMIN_ADDR"))
	setStr(&c.DBPath, getenv("KADR_DB_PATH"))
	setStr(&c.TmpDir, getenv("KADR_TMP_DIR"))
	setStr(&c.MasterKeyFile, getenv("KADR_MASTER_KEY_FILE"))
	setStr(&c.CFAccountID, getenv("KADR_CF_ACCOUNT_ID"))
	setStr(&c.CFAPIToken, getenv("KADR_CF_API_TOKEN"))
	setStr(&c.CFWorkerName, getenv("KADR_CF_WORKER_NAME"))
	setStr(&c.TelegramToken, getenv("KADR_TELEGRAM_BOT_TOKEN"))
	setStr(&c.TelegramChatID, getenv("KADR_TELEGRAM_CHAT_ID"))
	if dev {
		c.WebAuthnRPID = "localhost"
		c.WebAuthnOrigins = []string{"http://localhost:8081", "http://127.0.0.1:8081", "http://localhost:5173"}
	}
	setStr(&c.WebAuthnRPID, strings.TrimSpace(getenv("KADR_WEBAUTHN_RP_ID")))
	if v := strings.TrimSpace(getenv("KADR_WEBAUTHN_ORIGINS")); v != "" {
		c.WebAuthnOrigins = nil
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
				c.WebAuthnOrigins = append(c.WebAuthnOrigins, o)
			}
		}
	}
	setStr(&c.EdgeBaseURL, strings.TrimRight(getenv("KADR_EDGE_BASE_URL"), "/"))
	setStr(&c.AdminPublicURL, strings.TrimRight(getenv("KADR_ADMIN_PUBLIC_URL"), "/"))

	if p := getenv("KADR_API_KEY_PEPPER"); p != "" {
		c.APIKeyPepper = []byte(p)
	} else if dev {
		c.APIKeyPepper = []byte(DevPepper)
	}

	var err error
	if c.PoolImage, err = intEnv(getenv, "KADR_POOL_IMAGE", max(1, runtime.NumCPU()/2)); err != nil {
		return nil, err
	}
	if c.PoolVideo, err = intEnv(getenv, "KADR_POOL_VIDEO", 1); err != nil {
		return nil, err
	}
	if c.PoolFile, err = intEnv(getenv, "KADR_POOL_FILE", 1); err != nil {
		return nil, err
	}

	c.VideoHW = strings.ToLower(strings.TrimSpace(getenv("KADR_VIDEO_HW")))
	if c.VideoHW == "" {
		c.VideoHW = "auto"
	}
	switch c.VideoHW {
	case "auto", "vaapi", "off":
	default:
		return nil, fmt.Errorf("config: KADR_VIDEO_HW must be auto|vaapi|off, got %q", c.VideoHW)
	}

	c.LogLevel = slog.LevelInfo
	if dev {
		c.LogLevel = slog.LevelDebug
	}
	if lv := getenv("KADR_LOG_LEVEL"); lv != "" {
		if err := c.LogLevel.UnmarshalText([]byte(lv)); err != nil {
			return nil, fmt.Errorf("config: KADR_LOG_LEVEL: %w", err)
		}
	}
	return c, nil
}

// Validate checks that secrets required by serve/migrate/admin are present.
// In dev mode a missing master key file is not an error because it is
// generated on demand (see crypto.EnsureKeyFile).
func (c *Config) Validate() error {
	var errs []error
	if len(c.APIKeyPepper) == 0 {
		errs = append(errs, errors.New("KADR_API_KEY_PEPPER is not set (generate one with: head -c 32 /dev/urandom | base64)"))
	} else if len(c.APIKeyPepper) < MinPepperLen {
		errs = append(errs, fmt.Errorf("KADR_API_KEY_PEPPER must be at least %d characters", MinPepperLen))
	}
	if !c.Dev {
		if string(c.APIKeyPepper) == DevPepper {
			errs = append(errs, errors.New("KADR_API_KEY_PEPPER uses the dev pepper outside of dev mode"))
		}
		if c.MasterKeyFile == "" {
			errs = append(errs, errors.New("KADR_MASTER_KEY_FILE is not set"))
		} else if _, err := os.Stat(c.MasterKeyFile); err != nil {
			errs = append(errs, fmt.Errorf("master key file %s is not readable: %w (generate one with: head -c 32 /dev/urandom | base64 > %s)", c.MasterKeyFile, err, c.MasterKeyFile))
		}
	}
	if c.DBPath == "" {
		errs = append(errs, errors.New("KADR_DB_PATH is not set"))
	}
	if c.TmpDir == "" {
		errs = append(errs, errors.New("KADR_TMP_DIR is not set"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
}

// EnsureDirs creates the database and tmp directories when they are missing.
func (c *Config) EnsureDirs() error {
	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0o750); err != nil {
		return fmt.Errorf("config: create db dir: %w", err)
	}
	if err := os.MkdirAll(c.TmpDir, 0o750); err != nil {
		return fmt.Errorf("config: create tmp dir: %w", err)
	}
	return nil
}

func setStr(dst *string, v string) {
	if v = strings.TrimSpace(v); v != "" {
		*dst = v
	}
}

func parseBool(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

func intEnv(getenv Getenv, name string, def int) (int, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("config: %s must be a positive integer, got %q", name, v)
	}
	return n, nil
}
