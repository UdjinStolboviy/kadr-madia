package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func TestDevDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"KADR_DEV": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Dev || c.DBPath != filepath.Join("var", "kadr.db") || string(c.APIKeyPepper) != DevPepper || c.LogLevel != slog.LevelDebug {
		t.Fatalf("unexpected dev config %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("dev config should validate: %v", err)
	}
}

func TestProductionRequiresSecrets(t *testing.T) {
	c, err := Load(env(map[string]string{"KADR_MASTER_KEY_FILE": "/nonexistent/master.key"}))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "KADR_API_KEY_PEPPER") || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("err = %v", err)
	}
	c, _ = Load(env(map[string]string{"KADR_API_KEY_PEPPER": DevPepper, "KADR_MASTER_KEY_FILE": "/nonexistent"}))
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "dev pepper") {
		t.Fatalf("dev pepper accepted in production: %v", err)
	}
}

func TestOverridesAndErrors(t *testing.T) {
	c, err := Load(env(map[string]string{
		"KADR_PUBLIC_ADDR": "10.0.0.5:8080", "KADR_POOL_IMAGE": "3", "KADR_VIDEO_HW": "VAAPI", "KADR_LOG_LEVEL": "warn",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicAddr != "10.0.0.5:8080" || c.PoolImage != 3 || c.VideoHW != "vaapi" || c.LogLevel != slog.LevelWarn {
		t.Fatalf("overrides not applied: %+v", c)
	}
	for _, bad := range []map[string]string{
		{"KADR_POOL_VIDEO": "0"},
		{"KADR_VIDEO_HW": "nvenc"},
		{"KADR_LOG_LEVEL": "loud"},
		{"KADR_MULTIPART_THRESHOLD_MB": "4"},
		{"KADR_MULTIPART_THRESHOLD_MB": "5121"},
		{"KADR_MULTIPART_PART_SIZE_MB": "4"},
		{"KADR_MULTIPART_PART_SIZE_MB": "513"},
		{"KADR_MULTIPART_SESSION_HOURS": "0"},
		{"KADR_MULTIPART_SESSION_HOURS": "169"},
	} {
		if _, err := Load(env(bad)); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func TestMultipartConfigDefaultsAndOverrides(t *testing.T) {
	def, err := Load(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if def.MultipartThresholdBytes != 16<<20 || def.MultipartPartSizeBytes != 8<<20 || def.MultipartSessionTTL != 24*time.Hour {
		t.Fatalf("defaults: threshold=%d partSize=%d session=%v", def.MultipartThresholdBytes, def.MultipartPartSizeBytes, def.MultipartSessionTTL)
	}
	ovr, err := Load(env(map[string]string{
		"KADR_MULTIPART_THRESHOLD_MB":  "32",
		"KADR_MULTIPART_PART_SIZE_MB":  "16",
		"KADR_MULTIPART_SESSION_HOURS": "48",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if ovr.MultipartThresholdBytes != 32<<20 || ovr.MultipartPartSizeBytes != 16<<20 || ovr.MultipartSessionTTL != 48*time.Hour {
		t.Fatalf("overrides: threshold=%d partSize=%d session=%v", ovr.MultipartThresholdBytes, ovr.MultipartPartSizeBytes, ovr.MultipartSessionTTL)
	}
}
