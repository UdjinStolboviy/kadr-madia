package video

import (
	"bytes"
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

func TestVideoModesAreOrdered(t *testing.T) {
	e := engine(t)
	dir := t.TempDir()
	// Noisy content so that bitrate differences between modes show.
	src := filepath.Join(dir, "in.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"testsrc2=s=1920x1080:r=30:d=3,noise=alls=30:allf=t", "-f", "lavfi", "-i", "sine=d=3", "-c:v", "libx264", "-crf", "12",
		"-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sizes := map[string]int64{}
	widths := map[string]int{}
	for _, mode := range preset.Modes {
		var p preset.VideoParams
		for _, d := range preset.ModePresets() {
			if d.Slug == preset.ModeSlug("video", mode) {
				if err := jsonUnmarshal(d.Params, &p); err != nil {
					t.Fatal(err)
				}
			}
		}
		p.Video.Preset, p.Video.HW = "veryfast", "off" // speed only; rate control as in the mode
		info, err := e.Probe(context.Background(), src, p.Limits)
		if err != nil {
			t.Fatal(err)
		}
		wd := filepath.Join(dir, mode)
		_ = os.MkdirAll(wd, 0o755)
		out, err := e.Transcode(context.Background(), info, p, wd, nil)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		sizes[mode], widths[mode] = out.Bytes, out.Width
		t.Logf("%-8s %8d bytes %dpx", mode, out.Bytes, out.Width)
	}
	if !(sizes[preset.ModeCompact] < sizes[preset.ModeBalanced] && sizes[preset.ModeBalanced] < sizes[preset.ModeQuality]) {
		t.Fatalf("sizes not ordered: %v", sizes)
	}
	if widths[preset.ModeCompact] != 854 || widths[preset.ModeBalanced] != 1280 || widths[preset.ModeQuality] != 1920 {
		t.Fatalf("widths: %v", widths)
	}
}

func TestTranscodeAV1(t *testing.T) {
	e := engine(t)
	if out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output(); !bytes.Contains(out, []byte("libsvtav1")) {
		t.Skip("ffmpeg without libsvtav1")
	}
	dir := t.TempDir()
	src := gen(t, dir, "in.mp4", "-f", "lavfi", "-i", "sine=f=440:d=3", "-c:v", "libx264", "-c:a", "aac", "-shortest")
	p := videoParams(t)
	p.Video.Codec, p.Video.CRF = "av1", 38
	info, err := e.Probe(context.Background(), src, p.Limits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Transcode(context.Background(), info, p, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Encoder != "libsvtav1" || out.HWFallback || out.Width != 1280 {
		t.Fatalf("out %+v", out)
	}
	got, err := e.Probe(context.Background(), out.VideoPath, preset.VideoLimits{})
	if err != nil || got.VideoCodec != "av1" {
		t.Fatalf("output codec %q %v", got.VideoCodec, err)
	}
}

func TestSvtParams(t *testing.T) {
	cases := []struct {
		lp      int
		maxrate string
		want    string
	}{
		{0, "", ""},
		{2, "", "lp=2"},
		{0, "900k", "mbr=900"},
		{2, "4M", "lp=2:mbr=4000"},
		{0, "1.5M", "mbr=1500"},
		{0, "garbage", ""},
	}
	for _, c := range cases {
		if got := svtParams(c.lp, c.maxrate); got != c.want {
			t.Errorf("svtParams(%d, %q) = %q, want %q", c.lp, c.maxrate, got, c.want)
		}
	}
}
