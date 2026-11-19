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
	"time"
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
	DeliveryAddr    string // KADR_DELIVERY_ADDR — signed private delivery (origin mode); empty disables it
	DeliveryStreams int    // KADR_DELIVERY_MAX_STREAMS — concurrent delivery responses (default 64)
	DeliveryCDNTTL  int    // KADR_DELIVERY_CDN_MAX_AGE — seconds Cloudflare may cache a signed response (0 = off)
	DBPath          string
	TmpDir          string
	MasterKeyFile   string
	APIKeyPepper    []byte
	PoolImage       int
	PoolVideo       int
	PoolFile        int
	VideoAV1LP      int      // KADR_VIDEO_AV1_LP: SVT-AV1 level of parallelism (0 = auto); lower uses less memory
	VideoHW         string   // auto | vaapi | off
	TelegramToken   string   // KADR_TELEGRAM_BOT_TOKEN (alerts)
	TelegramChatID  string   // KADR_TELEGRAM_CHAT_ID
	EdgeBaseURL     string   // https://media.example.com — private delivery host (kadr-edge)
	CFAccountID     string   // KADR_CF_ACCOUNT_ID
	CFAPIToken      string   // KADR_CF_API_TOKEN
	CFWorkerName    string   // KADR_CF_WORKER_NAME (default kadr-edge)
	WebAuthnRPID    string   // KADR_WEBAUTHN_RP_ID (default localhost in dev; empty disables passkeys)
	WebAuthnOrigins []string // KADR_WEBAUTHN_ORIGINS, comma separated
	AdminPublicURL  string   // KADR_ADMIN_PUBLIC_URL — external admin URL (MCP endpoint, OAuth issuer, notification links)
	CFAccessTeam    string   // KADR_CF_ACCESS_TEAM_DOMAIN — e.g. myteam.cloudflareaccess.com; enables Access JWT checks
	CFAccessAUD     []string // KADR_CF_ACCESS_AUD — Application Audience tag(s), comma separated
	CFAccessExempt  []string // KADR_CF_ACCESS_EXEMPT — path prefixes served without an Access token
	AnthropicAPIKey string   // ANTHROPIC_API_KEY — enables AI moderation and scheduled agent runs
	AIModel                 string   // KADR_AI_MODEL (default claude-opus-5-5)
	AIClassifierURL         string   // KADR_AI_CLASSIFIER_URL — external moderation classifier (instead of Claude)
	MultipartThresholdBytes int64         // KADR_MULTIPART_THRESHOLD_MB (default 16 MiB, range 5–5120)
	MultipartPartSizeBytes  int64         // KADR_MULTIPART_PART_SIZE_MB (default 8 MiB, range 5–512)
	MultipartSessionTTL     time.Duration // KADR_MULTIPART_SESSION_HOURS (default 24h, range 1–168)
	LogLevel                slog.Level
	Dev                     bool
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
	setStr(&c.DeliveryAddr, strings.TrimSpace(getenv("KADR_DELIVERY_ADDR")))
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
	setStr(&c.CFAccessTeam, strings.TrimSpace(getenv("KADR_CF_ACCESS_TEAM_DOMAIN")))
	c.CFAccessAUD = splitList(getenv("KADR_CF_ACCESS_AUD"))
	c.CFAccessExempt = []string{"/healthz", "/readyz", "/mcp", "/admin/mcp", "/oauth/token", "/oauth/register", "/.well-known/"}
	if v := splitList(getenv("KADR_CF_ACCESS_EXEMPT")); v != nil {
		c.CFAccessExempt = v
	}
	setStr(&c.AnthropicAPIKey, strings.TrimSpace(getenv("ANTHROPIC_API_KEY")))
	c.AIModel = "claude-opus-5-5"
	setStr(&c.AIModel, strings.TrimSpace(getenv("KADR_AI_MODEL")))
	setStr(&c.AIClassifierURL, strings.TrimSpace(getenv("KADR_AI_CLASSIFIER_URL")))

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
	if c.DeliveryStreams, err = intEnv(getenv, "KADR_DELIVERY_MAX_STREAMS", 64); err != nil || c.DeliveryStreams < 1 || c.DeliveryStreams > 10000 {
		if err == nil {
			err = fmt.Errorf("KADR_DELIVERY_MAX_STREAMS must be 1–10000")
		}
		return nil, err
	}
	if c.DeliveryCDNTTL, err = intEnv(getenv, "KADR_DELIVERY_CDN_MAX_AGE", 0); err != nil || c.DeliveryCDNTTL < 0 || c.DeliveryCDNTTL > 7*24*3600 {
		if err == nil {
			err = fmt.Errorf("KADR_DELIVERY_CDN_MAX_AGE must be 0–604800 seconds")
		}
		return nil, err
	}
	if c.VideoAV1LP, err = intEnv(getenv, "KADR_VIDEO_AV1_LP", 0); err != nil || c.VideoAV1LP < 0 || c.VideoAV1LP > 64 {
		if err == nil {
			err = fmt.Errorf("KADR_VIDEO_AV1_LP must be 0–64")
		}
		return nil, err
	}
	mpThreshMB, err := intEnvRange(getenv, "KADR_MULTIPART_THRESHOLD_MB", 16, 5, 5120)
	if err != nil {
		return nil, err
	}
	c.MultipartThresholdBytes = int64(mpThreshMB) << 20
	mpPartMB, err := intEnvRange(getenv, "KADR_MULTIPART_PART_SIZE_MB", 8, 5, 512)
	if err != nil {
		return nil, err
	}
	c.MultipartPartSizeBytes = int64(mpPartMB) << 20
	mpHours, err := intEnvRange(getenv, "KADR_MULTIPART_SESSION_HOURS", 24, 1, 168)
	if err != nil {
		return nil, err
	}
	c.MultipartSessionTTL = time.Duration(mpHours) * time.Hour

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
	if (c.CFAccessTeam == "") != (len(c.CFAccessAUD) == 0) {
		errs = append(errs, errors.New("set both KADR_CF_ACCESS_TEAM_DOMAIN and KADR_CF_ACCESS_AUD, or neither"))
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

func intEnvRange(getenv Getenv, name string, def, minVal, maxVal int) (int, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minVal || n > maxVal {
		return 0, fmt.Errorf("config: %s must be %d–%d, got %q", name, minVal, maxVal, v)
	}
	return n, nil
}

// splitList splits a comma separated value, dropping empty items (nil when empty).
func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
