package video

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Config configures the ffmpeg engine.
type Config struct {
	FFmpeg  string // default "ffmpeg"
	FFprobe string // default "ffprobe"
	Nice    bool   // lower CPU priority of children
	Threads int    // x264 threads (default 2)
	// AV1LP is SVT-AV1's level of parallelism (0 = encoder default, about
	// one per core). Lower values cut peak memory (1080p: ~1.1 GB → ~0.9 GB
	// at 2) at a small speed cost.
	AV1LP  int
	Device string // VAAPI render node (default /dev/dri/renderD128)
	// TimeoutPerMediaSecond bounds a transcode to duration*TimeoutPerMediaSecond (default 6s per
	// second of video, at least MinTimeout).
	TimeoutPerMediaSecond time.Duration
	MinTimeout            time.Duration
	ProbeTimeout          time.Duration
}

// FFmpeg is the ffmpeg/ffprobe implementation of Engine.
type FFmpeg struct {
	cfg   Config
	vaapi bool
}

// New checks that ffmpeg and ffprobe are installed and detects VAAPI.
func New(cfg Config) (*FFmpeg, error) {
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.FFprobe == "" {
		cfg.FFprobe = "ffprobe"
	}
	if cfg.Threads <= 0 {
		cfg.Threads = 2
	}
	if cfg.Device == "" {
		cfg.Device = "/dev/dri/renderD128"
	}
	if cfg.TimeoutPerMediaSecond <= 0 {
		cfg.TimeoutPerMediaSecond = 6 * time.Second
	}
	if cfg.MinTimeout <= 0 {
		cfg.MinTimeout = 2 * time.Minute
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 20 * time.Second
	}
	for _, b := range []string{cfg.FFmpeg, cfg.FFprobe} {
		if _, err := exec.LookPath(b); err != nil {
			return nil, fmt.Errorf("%s not found: %w", b, err)
		}
	}
	e := &FFmpeg{cfg: cfg}
	e.vaapi = e.detectVAAPI()
	return e, nil
}

// HasVAAPI reports whether hardware encoding is usable.
func (e *FFmpeg) HasVAAPI() bool { return e.vaapi }

func (e *FFmpeg) detectVAAPI() bool {
	if _, err := os.Stat(e.cfg.Device); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.cfg.FFmpeg, "-hide_banner", "-nostdin", "-vaapi_device", e.cfg.Device,
		"-f", "lavfi", "-i", "color=c=black:s=256x256:d=0.1", "-vf", "format=nv12,hwupload",
		"-c:v", "h264_vaapi", "-frames:v", "1", "-f", "null", "-")
	return cmd.Run() == nil
}

type ffprobeOut struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		Duration     string `json:"duration"`
		Disposition  struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags     map[string]string `json:"tags"`
		SideData []struct {
			Rotation float64 `json:"rotation"`
		} `json:"side_data_list"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
	} `json:"format"`
}

// Probe implements Engine.
func (e *FFmpeg) Probe(ctx context.Context, path string, limits preset.VideoLimits) (SourceInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return SourceInfo{}, &Error{Code: CodeEngineFailure, Detail: "source is not readable", Retryable: true, Err: err}
	}
	if limits.MaxInputBytes > 0 && st.Size() > limits.MaxInputBytes {
		return SourceInfo{}, &Error{Code: CodeTooLarge, Detail: fmt.Sprintf("file is %d bytes, limit is %d", st.Size(), limits.MaxInputBytes)}
	}
	pctx, cancel := context.WithTimeout(ctx, e.cfg.ProbeTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(pctx, e.cfg.FFprobe, "-v", "error", "-print_format", "json", "-show_streams", "-show_format", path)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return SourceInfo{}, ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: "ffprobe could not read the file", Err: errors.New(strings.TrimSpace(stderr.String()))}
		}
		return SourceInfo{}, &Error{Code: CodeEngineFailure, Detail: "ffprobe failed to run", Retryable: true, Err: err}
	}
	var out ffprobeOut
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "unparseable ffprobe output", Err: err}
	}
	info := SourceInfo{Path: path, Bytes: st.Size(), Container: out.Format.FormatName}
	info.DurationSec, _ = strconv.ParseFloat(out.Format.Duration, 64)
	found := false
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			if found || s.Disposition.AttachedPic == 1 {
				continue
			}
			found = true
			info.VideoCodec, info.Width, info.Height = s.CodecName, s.Width, s.Height
			info.FPS = parseRate(s.AvgFrameRate)
			if info.DurationSec == 0 {
				info.DurationSec, _ = strconv.ParseFloat(s.Duration, 64)
			}
			rot := 0.0
			for _, sd := range s.SideData {
				if sd.Rotation != 0 {
					rot = sd.Rotation
				}
			}
			if r, err := strconv.Atoi(s.Tags["rotate"]); err == nil && rot == 0 {
				rot = float64(r)
			}
			info.Rotation = ((int(math.Round(rot)) % 360) + 360) % 360
			if info.Rotation == 90 || info.Rotation == 270 {
				info.Width, info.Height = info.Height, info.Width
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}
	if !found || info.Width <= 0 || info.Height <= 0 {
		return SourceInfo{}, &Error{Code: CodeUnsupportedMedia, Detail: "no video stream found"}
	}
	if info.DurationSec <= 0 {
		return SourceInfo{}, &Error{Code: CodeCorrupt, Detail: "could not determine the duration"}
	}
	if limits.MaxDurationSec > 0 && info.DurationSec > float64(limits.MaxDurationSec) {
		return SourceInfo{}, &Error{Code: CodeTooLong, Detail: fmt.Sprintf("video is %.0fs, limit is %ds", info.DurationSec, limits.MaxDurationSec)}
	}
	if (limits.MaxWidth > 0 && info.Width > limits.MaxWidth) || (limits.MaxHeight > 0 && info.Height > limits.MaxHeight) {
		return SourceInfo{}, &Error{Code: CodeTooManyPixels, Detail: fmt.Sprintf("%dx%d exceeds %dx%d", info.Width, info.Height, limits.MaxWidth, limits.MaxHeight)}
	}
	return info, nil
}

func parseRate(s string) float64 {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	n, _ := strconv.ParseFloat(a, 64)
	d, _ := strconv.ParseFloat(b, 64)
	if d == 0 {
		return 0
	}
	return n / d
}

// Transcode implements Engine.
func (e *FFmpeg) Transcode(ctx context.Context, src SourceInfo, p preset.VideoParams, workDir string, progress func(int)) (*Output, error) {
	av1 := p.Video.Codec == "av1"
	if p.Video.Codec != "" && p.Video.Codec != "h264" && !av1 {
		return nil, &Error{Code: CodeEngineFailure, Detail: "unsupported codec " + p.Video.Codec}
	}
	outPath := filepath.Join(workDir, "video.mp4")
	useHW := e.vaapi && p.Video.HW != "off" && !av1 // AV1 is encoded in software (SVT-AV1)
	if p.Video.HW == "vaapi" && !e.vaapi {
		useHW = false // requested but unavailable: fall back to software
	}
	timeout := time.Duration(src.DurationSec * float64(e.cfg.TimeoutPerMediaSecond))
	if timeout < e.cfg.MinTimeout {
		timeout = e.cfg.MinTimeout
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	enc := "libx264"
	if av1 {
		enc = "libsvtav1"
	}
	err := error(nil)
	var hwErr error
	if useHW {
		enc = "h264_vaapi"
		err = e.run(tctx, src, p, outPath, true, progress)
		if err != nil && ctx.Err() == nil && tctx.Err() == nil {
			enc, hwErr = "libx264", err // hardware failed: retry in software
			err = e.run(tctx, src, p, outPath, false, progress)
		}
	} else {
		err = e.run(tctx, src, p, outPath, false, progress)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tctx.Err() != nil {
			return nil, &Error{Code: CodeTimeout, Detail: "transcode timed out", Retryable: true, Err: err}
		}
		return nil, err
	}
	st, err := os.Stat(outPath)
	if err != nil || st.Size() == 0 {
		return nil, &Error{Code: CodeEngineFailure, Detail: "ffmpeg produced no output", Retryable: true, Err: err}
	}
	posterPath := filepath.Join(workDir, "poster.png")
	if err := e.poster(ctx, src, outPath, p.Poster.AtSec, posterPath); err != nil {
		return nil, err
	}
	res, err := e.Probe(ctx, outPath, preset.VideoLimits{})
	if err != nil {
		return nil, &Error{Code: CodeEngineFailure, Detail: "output failed verification", Retryable: true, Err: err}
	}
	if progress != nil {
		progress(100)
	}
	return &Output{
		VideoPath: outPath, PosterPath: posterPath, Bytes: st.Size(), Width: res.Width, Height: res.Height,
		DurationMs: int64(res.DurationSec * 1000), Encoder: enc, HasAudio: res.AudioCodec != "",
		HWFallback: hwErr != nil, HWError: errString(hwErr),
	}, nil
}

func (e *FFmpeg) run(ctx context.Context, src SourceInfo, p preset.VideoParams, out string, hw bool, progress func(int)) error {
	v := p.Video
	maxW := v.MaxWidth
	if maxW <= 0 {
		maxW = 1280
	}
	fps := v.FPS
	if fps <= 0 {
		fps = 30
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}
	if hw {
		args = append(args, "-vaapi_device", e.cfg.Device)
	}
	args = append(args, "-i", src.Path, "-map", "0:v:0", "-map", "0:a:0?", "-map_metadata", "-1", "-sn", "-dn")
	// Never upscale; width even for yuv420p.
	w := fmt.Sprintf("min(%d,iw)", maxW)
	if hw {
		args = append(args, "-vf", fmt.Sprintf("fps=%d,format=nv12,hwupload,scale_vaapi=w='%s':h=-2", fps, w),
			"-c:v", "h264_vaapi", "-rc_mode", "QVBR", "-global_quality", strconv.Itoa(v.CRF))
	} else if v.Codec == "av1" {
		// SVT-AV1: ~30–50 % smaller than H.264 at the same quality. A preset
		// maxrate becomes capped CRF (mbr, kbps) so high-motion clips cannot
		// blow the size; keyframe every 10 s for seeking.
		args = append(args, "-vf", fmt.Sprintf("scale='%s':-2:flags=lanczos,fps=%d", w, fps),
			"-c:v", "libsvtav1", "-pix_fmt", "yuv420p", "-crf", strconv.Itoa(v.CRF), "-preset", strconv.Itoa(svtPreset(v.Preset)),
			"-g", strconv.Itoa(fps*10))
		if sp := svtParams(e.cfg.AV1LP, v.Maxrate); sp != "" {
			args = append(args, "-svtav1-params", sp)
		}
	} else {
		args = append(args, "-vf", fmt.Sprintf("scale='%s':-2:flags=lanczos,fps=%d", w, fps),
			"-c:v", "libx264", "-profile:v", "high", "-level", "4.1", "-pix_fmt", "yuv420p",
			"-crf", strconv.Itoa(v.CRF), "-preset", orDefault(v.Preset, "slow"), "-threads", strconv.Itoa(e.cfg.Threads))
	}
	if v.Maxrate != "" && v.Codec != "av1" {
		args = append(args, "-maxrate", v.Maxrate)
		args = append(args, "-bufsize", orDefault(v.Bufsize, v.Maxrate))
	}
	if src.AudioCodec != "" {
		a := p.Audio
		args = append(args, "-c:a", "aac", "-b:a", orDefault(a.Bitrate, "64k"))
		if a.Channels > 0 {
			args = append(args, "-ac", strconv.Itoa(a.Channels))
		}
		if a.SampleRate > 0 {
			args = append(args, "-ar", strconv.Itoa(a.SampleRate))
		}
	}
	args = append(args, "-movflags", "+faststart", "-progress", "pipe:1", "-nostats", out)
	return e.exec(ctx, args, src.DurationSec, progress)
}

// exec runs ffmpeg with args (which must include "-progress pipe:1"),
// reporting progress against durationSec and classifying failures.
func (e *FFmpeg) exec(ctx context.Context, args []string, durationSec float64, progress func(int)) error {
	var cmd *exec.Cmd
	if e.cfg.Nice {
		cmd = exec.CommandContext(ctx, "nice", append([]string{"-n", "10", e.cfg.FFmpeg}, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, e.cfg.FFmpeg, args...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return &Error{Code: CodeEngineFailure, Detail: "ffmpeg failed to start", Retryable: true, Err: err}
	}
	sc := bufio.NewScanner(stdout)
	last := -1
	for sc.Scan() {
		k, val, ok := strings.Cut(sc.Text(), "=")
		if !ok || progress == nil || (k != "out_time_us" && k != "out_time_ms") {
			continue
		}
		us, err := strconv.ParseInt(val, 10, 64)
		if err != nil || durationSec <= 0 {
			continue
		}
		pct := int(float64(us) / 1e6 / durationSec * 100)
		if pct > 99 {
			pct = 99
		}
		if pct > last && pct >= 0 {
			last = pct
			progress(pct)
		}
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		code, retry := CodeCorrupt, false
		if strings.Contains(msg, "No such file") || strings.Contains(msg, "Cannot allocate memory") || strings.Contains(msg, "signal") {
			code, retry = CodeEngineFailure, true
		}
		return &Error{Code: code, Detail: "ffmpeg failed", Retryable: retry, Err: errors.New(msg)}
	}
	return nil
}

// poster grabs one frame from the encoded video (already rotated/scaled).
func (e *FFmpeg) poster(ctx context.Context, src SourceInfo, video string, at float64, out string) error {
	if at < 0 || at >= src.DurationSec {
		at = 0
	}
	if at > src.DurationSec/2 { // very short clips: use the middle
		at = src.DurationSec / 2
	}
	try := func(ts float64) error {
		cmd := exec.CommandContext(ctx, e.cfg.FFmpeg, "-hide_banner", "-nostdin", "-y", "-loglevel", "error",
			"-ss", strconv.FormatFloat(ts, 'f', 3, 64), "-i", video, "-frames:v", "1", out)
		return cmd.Run()
	}
	if err := try(at); err != nil || !nonEmpty(out) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := try(0); err != nil || !nonEmpty(out) {
			return &Error{Code: CodeEngineFailure, Detail: "could not extract a poster frame", Retryable: true, Err: err}
		}
	}
	return nil
}

func nonEmpty(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

// svtPreset maps x264 preset names to SVT-AV1 presets (higher = faster).
// svtParams builds -svtav1-params: level of parallelism and the capped-CRF
// maximum bitrate from a maxrate like "900k" or "4M".
func svtParams(lp int, maxrate string) string {
	var p []string
	if lp > 0 {
		p = append(p, "lp="+strconv.Itoa(lp))
	}
	if kbps := bitrateKbps(maxrate); kbps > 0 {
		p = append(p, "mbr="+strconv.Itoa(kbps))
	}
	return strings.Join(p, ":")
}

func bitrateKbps(s string) int {
	s = strings.TrimSpace(strings.ToLower(s))
	mul := 0.001
	switch {
	case strings.HasSuffix(s, "k"):
		mul, s = 1, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mul, s = 1000, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int(f*mul + 0.5)
}

func svtPreset(x264 string) int {
	switch x264 {
	case "ultrafast":
		return 12
	case "superfast":
		return 11
	case "veryfast":
		return 10
	case "faster":
		return 9
	case "fast":
		return 8
	case "medium":
		return 7
	case "slower":
		return 5
	}
	return 6 // slow (default)
}
