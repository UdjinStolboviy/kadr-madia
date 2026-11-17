package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func TestPlanLadder(t *testing.T) {
	names := func(v []HLSVariant) string {
		var s []string
		for _, x := range v {
			s = append(s, x.Name)
		}
		return strings.Join(s, ",")
	}
	cases := []struct {
		w, h, maxW int
		want       string
	}{
		{1920, 1080, 4096, "360p,480p,720p,1080p"},
		{1920, 1080, 1280, "360p,480p,720p"},
		{1280, 720, 0, "360p,480p,720p"},
		{854, 480, 0, "360p,480p"},
		{320, 240, 0, "240p"},
		{1080, 1920, 4096, "360p,480p,720p,1080p"},
	}
	for _, c := range cases {
		if got := names(planLadder(c.w, c.h, c.maxW)); got != c.want {
			t.Errorf("%dx%d max %d: got %s want %s", c.w, c.h, c.maxW, got, c.want)
		}
	}
	for _, v := range planLadder(1920, 1080, 4096) {
		if v.Width%2 != 0 || v.Height%2 != 0 {
			t.Errorf("odd size %+v", v)
		}
	}
}

func TestPackageHLS(t *testing.T) {
	e, err := New(Config{})
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=25:d=9",
		"-f", "lavfi", "-i", "sine=d=9", "-c:v", "libx264", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ctx := context.Background()
	info, err := e.Probe(ctx, src, preset.VideoLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var last int
	out, err := e.PackageHLS(ctx, info, preset.VideoParams{Kind: "video", Video: preset.VideoCodec{Codec: "h264", MaxWidth: 1280, FPS: 25, CRF: 30, Preset: "ultrafast"}, HLS: true},
		dir, func(p int) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if last != 100 {
		t.Fatalf("progress %d", last)
	}
	if len(out.Variants) != 3 {
		t.Fatalf("variants: %+v", out.Variants)
	}
	master, err := os.ReadFile(filepath.Join(out.Dir, out.Master))
	if err != nil {
		t.Fatal(err)
	}
	m := string(master)
	if !strings.HasPrefix(m, "#EXTM3U") || strings.Count(m, "#EXT-X-STREAM-INF") != 3 || !strings.Contains(m, "CODECS=") {
		t.Fatalf("master:\n%s", m)
	}
	if !strings.Contains(m, "mp4a") {
		t.Fatalf("audio missing from variants:\n%s", m)
	}
	var segs int
	for _, f := range out.Files {
		if strings.HasSuffix(f, ".m4s") {
			segs++
		}
	}
	if segs < 3*2 { // 9 s at 4 s segments: 3 per variant
		t.Fatalf("segments: %d (%v)", segs, out.Files)
	}
	// Variant playlists must reference fMP4 init + segments and be ffprobe-readable.
	var vp string
	for _, f := range out.Files {
		if strings.HasSuffix(f, ".m3u8") && f != out.Master {
			vp = f
			b, _ := os.ReadFile(filepath.Join(out.Dir, f))
			if !strings.Contains(string(b), "#EXT-X-MAP") || !strings.Contains(string(b), "#EXT-X-ENDLIST") {
				t.Fatalf("variant %s:\n%s", f, b)
			}
		}
	}
	vinfo, err := e.Probe(ctx, filepath.Join(out.Dir, vp), preset.VideoLimits{})
	if err != nil {
		t.Fatalf("ffprobe variant: %v", err)
	}
	if vinfo.AudioCodec == "" || vinfo.DurationSec < 8 {
		t.Fatalf("variant info %+v", vinfo)
	}
}
