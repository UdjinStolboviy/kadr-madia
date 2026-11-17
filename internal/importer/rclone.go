package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Rclone downloads single files from cloud drives on demand. The OAuth token
// is passed through the environment of the child process (no config file on
// disk) and the child gets a minimal environment.
type Rclone struct {
	Bin string // default "rclone"
}

var rcloneTypes = map[string]string{"gdrive": "drive", "onedrive": "onedrive", "dropbox": "dropbox"}

// Available reports whether the rclone binary can be found.
func (r *Rclone) Available() bool {
	_, err := exec.LookPath(r.bin())
	return err == nil
}

func (r *Rclone) bin() string {
	if r.Bin == "" {
		return "rclone"
	}
	return r.Bin
}

// Fetch copies the file identified by ref (a Drive file id for gdrive, a path
// for onedrive/dropbox) from the account described by provider/tokenJSON into
// destPath and returns its name in the drive. At most maxBytes are accepted.
func (r *Rclone) Fetch(ctx context.Context, provider, tokenJSON, ref, destPath string, maxBytes int64) (string, error) {
	typ, ok := rcloneTypes[provider]
	if !ok {
		return "", fmt.Errorf("importer: unsupported provider %q", provider)
	}
	if ref == "" || strings.ContainsAny(ref, "\x00\n") || strings.HasPrefix(ref, "-") {
		return "", errors.New("importer: invalid file reference")
	}
	dir, err := os.MkdirTemp(filepath.Dir(destPath), "rclone-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	args := []string{"--config", os.DevNull, "--retries", "2", "--low-level-retries", "3"}
	if maxBytes > 0 {
		args = append(args, "--max-size", strconv.FormatInt(maxBytes, 10))
	}
	if provider == "gdrive" {
		args = append(args, "backend", "copyid", "KADR:", ref, dir+"/")
	} else {
		args = append(args, "copyto", "KADR:"+ref, filepath.Join(dir, filepath.Base(ref)))
	}
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir,
		"RCLONE_CONFIG_KADR_TYPE=" + typ, "RCLONE_CONFIG_KADR_TOKEN=" + tokenJSON,
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("importer: rclone failed: %s", scrub(stderr.String(), tokenJSON))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].IsDir() {
		return "", errors.New("importer: rclone did not produce exactly one file (is the reference a folder or over the size limit?)")
	}
	if err := os.Rename(filepath.Join(dir, entries[0].Name()), destPath); err != nil {
		return "", err
	}
	return entries[0].Name(), nil
}

func scrub(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "***")
	}
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
