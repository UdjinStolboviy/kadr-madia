package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// HLS packaging constants.
const (
	// HLSSegmentSec is the target segment duration (keyframe interval).
	HLSSegmentSec = 4
	// HLSMaxVariants bounds the ladder size.
	HLSMaxVariants = 4
	hlsDir         = "hls"
	hlsMaster      = "master.m3u8"
)

var _ HLSEngine = (*FFmpeg)(nil)

type hlsRung struct {
	short       int // short side in pixels
	maxrateKbps int
}

// hlsRungs is the ladder, keyed by the short side of the picture, so that
// portrait video gets the same quality tiers as landscape.
var hlsRungs = []hlsRung{{360, 800}, {480, 1400}, {720, 2800}, {1080, 5000}}

// planLadder picks the variants for a source: rungs not larger than the
// source's short side and not wider than maxW (preset max width, default
// 1280 when the preset sets none). At least one variant is always
// returned (the source size, downscaled to maxW if needed).
func planLadder(srcW, srcH, maxW int) []HLSVariant {
	if maxW <= 0 {
		maxW = 1280
	}
	short := min(srcW, srcH)
	var out []HLSVariant
	for _, r := range hlsRungs {
		if r.short > short {
			continue
		}
		w, h := scaleShort(srcW, srcH, r.short)
		if w > maxW {
			continue
		}
		out = append(out, HLSVariant{Name: fmt.Sprintf("%dp", r.short), Width: w, Height: h, MaxrateKbps: r.maxrateKbps})
	}
	if len(out) > HLSMaxVariants {
		out = out[len(out)-HLSMaxVariants:]
	}
	if len(out) == 0 {
		// Source smaller than the lowest rung (or wider than maxW at it): one
		// variant at the source size, never upscaled.
		w, h := srcW, srcH
		if w > maxW {
			w, h = maxW, srcH*maxW/srcW
		}
		w, h = w&^1, h&^1
		if w < 2 {
			w = 2
		}
		if h < 2 {
			h = 2
		}
		out = []HLSVariant{{Name: fmt.Sprintf("%dp", min(w, h)), Width: w, Height: h, MaxrateKbps: 800}}
	}
	return out
}

// scaleShort scales (w,h) so the short side becomes short; both sides even.
func scaleShort(w, h, short int) (int, int) {
	if w <= h {
		return short, (h*short/w + 1) &^ 1
	}
	return (w*short/h + 1) &^ 1, short
}

// PackageHLS implements HLSEngine: one ffmpeg run encodes every variant
// (software x264; VAAPI is not used for the ladder) into keyframe-aligned
// fMP4 segments with audio in each variant.
func (e *FFmpeg) PackageHLS(ctx context.Context, src SourceInfo, p preset.VideoParams, workDir string, progress func(int)) (*HLSOutput, error) {
	if p.Video.Codec != "" && p.Video.Codec != "h264" {
		return nil, &Error{Code: CodeEngineFailure, Detail: "unsupported codec " + p.Video.Codec}
	}
	dir := filepath.Join(workDir, hlsDir)
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, &Error{Code: CodeEngineFailure, Detail: "cannot create hls dir", Retryable: true, Err: err}
	}
	vars := planLadder(src.Width, src.Height, p.Video.MaxWidth)
	fps := p.Video.FPS
	if fps <= 0 {
		fps = 30
	}
	gop := fps * HLSSegmentSec
	crf := p.Video.CRF
	if crf <= 0 {
		crf = 24
	}
	hasAudio := src.AudioCodec != ""

	// Filter graph: fps, split, scale each branch.
	n := len(vars)
	var fg strings.Builder
	fmt.Fprintf(&fg, "[0:v:0]fps=%d,split=%d", fps, n)
	for i := range vars {
		fmt.Fprintf(&fg, "[s%d]", i)
	}
	for i, v := range vars {
		fmt.Fprintf(&fg, ";[s%d]scale=%d:%d:flags=lanczos[v%d]", i, v.Width, v.Height, i)
	}

	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-i", src.Path, "-filter_complex", fg.String()}
	for i := range vars {
		args = append(args, "-map", fmt.Sprintf("[v%d]", i))
	}
	if hasAudio {
		for range vars {
			args = append(args, "-map", "0:a:0")
		}
	}
	args = append(args, "-map_metadata", "-1", "-sn", "-dn",
		"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p", "-preset", hlsX264Preset(p.Video.Preset),
		"-crf", fmt.Sprint(crf), "-threads", fmt.Sprint(e.cfg.Threads),
		"-g", fmt.Sprint(gop), "-keyint_min", fmt.Sprint(gop), "-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", HLSSegmentSec))
	for i, v := range vars {
		args = append(args, fmt.Sprintf("-maxrate:v:%d", i), fmt.Sprintf("%dk", v.MaxrateKbps),
			fmt.Sprintf("-bufsize:v:%d", i), fmt.Sprintf("%dk", v.MaxrateKbps*2))
	}
	var vsm []string
	for i, v := range vars {
		m := fmt.Sprintf("v:%d", i)
		if hasAudio {
			m += fmt.Sprintf(",a:%d", i)
		}
		vsm = append(vsm, m+",name:"+v.Name)
	}
	if hasAudio {
		a := p.Audio
		args = append(args, "-c:a", "aac", "-b:a", orDefault(a.Bitrate, "64k"))
		if a.Channels > 0 {
			args = append(args, "-ac", fmt.Sprint(a.Channels))
		}
		if a.SampleRate > 0 {
			args = append(args, "-ar", fmt.Sprint(a.SampleRate))
		}
	}
	args = append(args,
		"-f", "hls", "-hls_time", fmt.Sprint(HLSSegmentSec), "-hls_playlist_type", "vod", "-hls_list_size", "0",
		"-hls_segment_type", "fmp4", "-hls_flags", "independent_segments",
		"-hls_fmp4_init_filename", "init_%v.mp4",
		"-hls_segment_filename", filepath.Join(dir, "seg_%v_%05d.m4s"),
		"-master_pl_name", hlsMaster,
		"-var_stream_map", strings.Join(vsm, " "),
		"-progress", "pipe:1", "-nostats", filepath.Join(dir, "v_%v.m3u8"))

	timeout := time.Duration(src.DurationSec * float64(e.cfg.TimeoutPerSec) * float64(n+1) / 2)
	if timeout < e.cfg.MinTimeout {
		timeout = e.cfg.MinTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := e.exec(tctx, args, src.DurationSec, progress); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tctx.Err() != nil {
			return nil, &Error{Code: CodeTimeout, Detail: "hls packaging timed out", Retryable: true, Err: err}
		}
		return nil, err
	}
	out, err := collectHLS(dir, vars)
	if err != nil {
		return nil, err
	}
	out.Encoder = "libx264"
	if progress != nil {
		progress(100)
	}
	return out, nil
}

// hlsX264Preset avoids the very slow presets for a multi-variant encode.
func hlsX264Preset(p string) string {
	switch p {
	case "", "slow", "slower", "veryslow", "placebo":
		return "medium"
	}
	return p
}

// collectHLS verifies and inventories the produced package.
func collectHLS(dir string, vars []HLSVariant) (*HLSOutput, error) {
	bad := func(detail string, err error) (*HLSOutput, error) {
		return nil, &Error{Code: CodeEngineFailure, Detail: detail, Retryable: true, Err: err}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return bad("hls output is not readable", err)
	}
	out := &HLSOutput{Dir: dir, Master: hlsMaster, Variants: vars}
	var names []string
	for _, en := range ents {
		if en.IsDir() {
			continue
		}
		st, err := en.Info()
		if err != nil {
			return bad("hls output is not readable", err)
		}
		if st.Size() == 0 {
			return bad("hls output contains an empty file "+en.Name(), nil)
		}
		out.Bytes += st.Size()
		if en.Name() != hlsMaster {
			names = append(names, en.Name())
		}
	}
	sort.Strings(names)
	if out.Bytes == 0 || !nonEmpty(filepath.Join(dir, hlsMaster)) {
		return bad("ffmpeg produced no master playlist", nil)
	}
	out.Files = append([]string{hlsMaster}, names...)
	var playlists, inits, segs int
	for _, n := range names {
		switch {
		case strings.HasSuffix(n, ".m3u8"):
			playlists++
		case strings.HasPrefix(n, "init_"):
			inits++
		case strings.HasSuffix(n, ".m4s"):
			segs++
		}
	}
	if playlists != len(vars) || inits != len(vars) || segs < len(vars) {
		return bad(fmt.Sprintf("incomplete hls output: %d playlists, %d init, %d segments for %d variants", playlists, inits, segs, len(vars)), nil)
	}
	return out, nil
}
