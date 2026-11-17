package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRcloneFetchWithFakeBinary(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "rclone")
	script := `#!/bin/sh
# last arg is destination (dir/ for copyid, file for copyto)
for a; do last="$a"; done
case "$last" in */) printf data > "${last}photo.jpg";; *) printf data > "$last";; esac
[ "$RCLONE_CONFIG_KADR_TOKEN" = '{"access_token":"x"}' ] || exit 3
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Rclone{Bin: fake}
	for _, tc := range []struct{ prov, ref string }{{"gdrive", "1AbC"}, {"dropbox", "/pics/photo.jpg"}} {
		dest := filepath.Join(dir, "out-"+tc.prov)
		name, err := r.Fetch(context.Background(), tc.prov, `{"access_token":"x"}`, tc.ref, dest, 1<<20)
		if err != nil || name != "photo.jpg" {
			t.Fatalf("%s: %q %v", tc.prov, name, err)
		}
		if b, _ := os.ReadFile(dest); string(b) != "data" {
			t.Fatalf("content %q", b)
		}
	}
	if _, err := r.Fetch(context.Background(), "gdrive", `{"bad":1}`, "1AbC", filepath.Join(dir, "o3"), 0); err == nil || strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("expected scrubbed failure, got %v", err)
	}
	if _, err := r.Fetch(context.Background(), "box", "{}", "x", filepath.Join(dir, "o4"), 0); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	if _, err := r.Fetch(context.Background(), "gdrive", "{}", "--evil", filepath.Join(dir, "o5"), 0); err == nil {
		t.Fatal("option injection accepted")
	}
}
