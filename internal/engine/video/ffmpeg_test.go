package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func gen(t *testing.T, dir, name string, extra ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=1920x1080:r=25:d=3"}
	args = append(args, extra...)
	args = append(args, p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("gen: %v %s", err, out)
	}
	return p
}

func engine(t *testing.T) *FFmpeg {
	e, err := New(Config{})
	if err != nil {
		t.Skip("ffmpeg unavailable:", err)
	}
	return e
}

func videoParams(t *testing.T) preset.VideoParams {
	for _, d := range preset.DefaultPresets() {
		if d.Slug == "video-720p" {
			var p preset.VideoParams
			if err := jsonUnmarshal(d.Params, &p); err != nil {
				t.Fatal(err)
			}
			p.Video.Preset = "veryfast"
			p.Video.HW = "off"
			return p
		}
	}
	t.Fatal("no preset")
	return preset.VideoParams{}
}

func TestTranscode720p(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	src := gen(t, dir, "in.mp4", "-f", "lavfi", "-i", "sine=f=440:d=3", "-c:v", "libx264", "-c:a", "aac", "-shortest")
	p := videoParams(t)
	ctx := context.Background()
	info, err := e.Probe(ctx, src, p.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 1920 || info.Height != 1080 || info.AudioCodec == "" {
		t.Fatalf("info %+v", info)
	}
	var last int
	out, err := e.Transcode(ctx, info, p, dir, func(pct int) { last = pct })
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 1280 || out.Height != 720 || !out.HasAudio || out.Encoder != "libx264" {
		t.Fatalf("out %+v", out)
	}
	if last != 100 {
		t.Fatalf("progress %d", last)
	}
	if st, err := os.Stat(out.PosterPath); err != nil || st.Size() == 0 {
		t.Fatal("no poster")
	}
	// faststart: moov before mdat
	b, _ := os.ReadFile(out.VideoPath)
	if i, j := indexOf(b, "moov"), indexOf(b, "mdat"); i < 0 || j < 0 || i > j {
		t.Fatalf("not faststart moov=%d mdat=%d", i, j)
	}
}

func TestProbeLimitsAndGarbage(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	src := gen(t, dir, "in.mp4", "-c:v", "libx264")
	cases := []struct {
		name   string
		limits preset.VideoLimits
		code   string
	}{
		{"long", preset.VideoLimits{MaxDurationSec: 1}, CodeTooLong},
		{"big", preset.VideoLimits{MaxInputBytes: 10}, CodeTooLarge},
		{"pixels", preset.VideoLimits{MaxWidth: 640, MaxHeight: 480}, CodeTooManyPixels},
	}
	for _, c := range cases {
		_, err := e.Probe(context.Background(), src, c.limits)
		if ie, ok := AsError(err); !ok || ie.Code != c.code {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	junk := filepath.Join(dir, "junk.mp4")
	_ = os.WriteFile(junk, []byte("definitely not a video"), 0o600)
	if _, err := e.Probe(context.Background(), junk, preset.VideoLimits{}); err == nil {
		t.Fatal("garbage accepted")
	} else if ie, ok := AsError(err); !ok || ie.Code != CodeUnsupportedMedia {
		t.Fatalf("garbage: %v", err)
	}
}

func TestRotatedNoAudioNoUpscale(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	plain := gen(t, dir, "plain.mp4", "-vf", "scale=640:360", "-c:v", "libx264")
	src := filepath.Join(dir, "rot.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-display_rotation", "90", "-i", plain, "-c", "copy", src).CombinedOutput(); err != nil {
		t.Fatalf("rotate: %v %s", err, out)
	}
	p := videoParams(t)
	info, err := e.Probe(context.Background(), src, p.Limits)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 360 || info.Height != 640 {
		t.Fatalf("rotation not applied in probe: %+v", info)
	}
	out, err := e.Transcode(context.Background(), info, p, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.HasAudio || out.Width > 640 {
		t.Fatalf("out %+v", out)
	}
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}
