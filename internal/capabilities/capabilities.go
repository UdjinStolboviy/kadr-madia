// Package capabilities probes the external tools Kadr depends on (libvips,
// ffmpeg, ffprobe, rclone, VAAPI). Results feed `kadr doctor` and
// GET /admin/api/system. Child processes are run without a shell.
package capabilities

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Cap is the result of one probe.
type Cap struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Report holds every probe result.
type Report struct {
	Vips     Cap `json:"vips"`
	VipsAVIF Cap `json:"vips_avif"`
	FFmpeg   Cap `json:"ffmpeg"`
	FFprobe  Cap `json:"ffprobe"`
	Rclone   Cap `json:"rclone"`
	VAAPI    Cap `json:"vaapi"`
	// Ghostscript and qpdf are optional: they enable PDF thumbnails and optimisation.
	Ghostscript Cap `json:"ghostscript"`
	QPDF        Cap `json:"qpdf"`
}

// RenderDevice is the Intel iGPU render node used for VAAPI.
const RenderDevice = "/dev/dri/renderD128"

const probeTimeout = 15 * time.Second

// Probe runs all checks. tmpDir is used for the AVIF encode test.
func Probe(ctx context.Context, tmpDir string) Report {
	var r Report
	var wg sync.WaitGroup
	wg.Add(6)
	go func() {
		defer wg.Done()
		r.Vips = versionProbe(ctx, "vips", []string{"--version"}, func(out string) string {
			// "vips-8.16.0" (possibly followed by build info)
			f := firstField(out)
			return strings.TrimPrefix(f, "vips-")
		})
		if r.Vips.OK {
			r.VipsAVIF = avifProbe(ctx, tmpDir)
		} else {
			r.VipsAVIF = Cap{Detail: "vips not available"}
		}
	}()
	go func() {
		defer wg.Done()
		r.FFmpeg = versionProbe(ctx, "ffmpeg", []string{"-hide_banner", "-version"}, ffVersion)
		r.VAAPI = vaapiProbe(ctx, r.FFmpeg.OK)
	}()
	go func() {
		defer wg.Done()
		r.FFprobe = versionProbe(ctx, "ffprobe", []string{"-hide_banner", "-version"}, ffVersion)
	}()
	go func() {
		defer wg.Done()
		r.Rclone = versionProbe(ctx, "rclone", []string{"version"}, func(out string) string {
			// "rclone v1.69.1"
			f := strings.Fields(firstLine(out))
			if len(f) >= 2 {
				return strings.TrimPrefix(f[1], "v")
			}
			return ""
		})
	}()
	go func() {
		defer wg.Done()
		r.Ghostscript = versionProbe(ctx, "gs", []string{"--version"}, func(out string) string { return strings.TrimSpace(firstLine(out)) })
	}()
	go func() {
		defer wg.Done()
		r.QPDF = versionProbe(ctx, "qpdf", []string{"--version"}, func(out string) string {
			f := strings.Fields(firstLine(out)) // "qpdf version 12.2.0"
			if len(f) >= 3 {
				return f[2]
			}
			return ""
		})
	}()
	wg.Wait()
	return r
}

func ffVersion(out string) string {
	// "ffmpeg version 7.1.1 Copyright ..." / "ffprobe version n7.1 ..."
	f := strings.Fields(firstLine(out))
	if len(f) >= 3 && f[1] == "version" {
		return f[2]
	}
	return ""
}

func versionProbe(ctx context.Context, bin string, args []string, parse func(string) string) Cap {
	path, err := exec.LookPath(bin)
	if err != nil {
		return Cap{Detail: bin + " not found in PATH"}
	}
	out, err := run(ctx, path, args...)
	if err != nil {
		return Cap{Detail: fmt.Sprintf("%s failed: %v", bin, err)}
	}
	return Cap{OK: true, Version: parse(out), Detail: path}
}

// avifProbe encodes a tiny generated image with heifsave compression=av1.
func avifProbe(ctx context.Context, tmpDir string) Cap {
	dir, err := os.MkdirTemp(tmpDir, "probe-avif-")
	if err != nil {
		return Cap{Detail: fmt.Sprintf("create temp dir: %v", err)}
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "in.v")
	dst := filepath.Join(dir, "out.avif")
	if _, err := run(ctx, "vips", "black", src, "16", "16", "--bands", "3"); err != nil {
		return Cap{Detail: fmt.Sprintf("generate test image: %v", err)}
	}
	// `black --bands 3` is a "multiband" image; heifsave needs a colour space.
	rgb := filepath.Join(dir, "rgb.v")
	if _, err := run(ctx, "vips", "copy", src, rgb, "--interpretation", "srgb"); err != nil {
		return Cap{Detail: fmt.Sprintf("generate test image: %v", err)}
	}
	if _, err := run(ctx, "vips", "heifsave", rgb, dst, "--compression", "av1", "--Q", "50"); err != nil {
		return Cap{Detail: fmt.Sprintf("heifsave compression=av1 failed: %v", err)}
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return Cap{Detail: "heifsave produced no output"}
	}
	return Cap{OK: true, Detail: fmt.Sprintf("encoded 16x16 AVIF (%d B)", st.Size())}
}

// vaapiProbe checks the render node and whether ffmpeg has h264_vaapi.
func vaapiProbe(ctx context.Context, haveFFmpeg bool) Cap {
	if _, err := os.Stat(RenderDevice); err != nil {
		return Cap{Detail: RenderDevice + " not present (software encoding)"}
	}
	f, err := os.OpenFile(RenderDevice, os.O_RDWR, 0)
	if err != nil {
		return Cap{Detail: fmt.Sprintf("%s present but not accessible: %v", RenderDevice, err)}
	}
	f.Close()
	if !haveFFmpeg {
		return Cap{OK: true, Detail: RenderDevice + " present; ffmpeg missing"}
	}
	out, err := run(ctx, "ffmpeg", "-hide_banner", "-encoders")
	if err != nil {
		return Cap{OK: true, Detail: RenderDevice + " present; could not list encoders"}
	}
	if !strings.Contains(out, "h264_vaapi") {
		return Cap{Detail: RenderDevice + " present but ffmpeg lacks h264_vaapi"}
	}
	return Cap{OK: true, Detail: RenderDevice + " present, h264_vaapi available"}
}

func run(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(firstLine(errb.String()))
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = "timed out"
		}
		if msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	if out.Len() == 0 {
		return errb.String(), nil
	}
	return out.String(), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// Holder caches a Report computed in the background at startup.
type Holder struct {
	mu     sync.RWMutex
	report Report
	done   bool
}

// NewHolder returns a holder whose report says "probing" until Set is called.
func NewHolder() *Holder {
	pending := Cap{Detail: "probe in progress"}
	return &Holder{report: Report{Vips: pending, VipsAVIF: pending, FFmpeg: pending, FFprobe: pending, Rclone: pending, VAAPI: pending, Ghostscript: pending, QPDF: pending}}
}

// Set stores the final report.
func (h *Holder) Set(r Report) {
	h.mu.Lock()
	h.report, h.done = r, true
	h.mu.Unlock()
}

// Ready reports whether the startup probe has finished.
func (h *Holder) Ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.done
}

// Get returns the current report.
func (h *Holder) Get() Report {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.report
}
