package image

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// VipsConfig configures the libvips CLI engine.
type VipsConfig struct {
	// VipsPath and VipsHeaderPath are the `vips` and `vipsheader` binaries.
	// Empty values are resolved from PATH.
	VipsPath       string
	VipsHeaderPath string
	// Nice runs every child process under `nice -n 10`.
	Nice bool
	// Timeout bounds one Probe or Process call (default 30s,
	// docs/SECURITY.md §5).
	Timeout time.Duration
	// MaxConcurrency bounds how many Probe/Process calls run at once
	// (default 2).
	MaxConcurrency int
	// ThreadsPerJob is VIPS_CONCURRENCY for child processes (default 1).
	// It also sets the AV1 encoder thread count. Cores used ≈
	// MaxConcurrency × ThreadsPerJob.
	ThreadsPerJob int
}

// VipsCLI implements Engine by shelling out to the libvips command line
// tools. It is safe for concurrent use.
type VipsCLI struct {
	vips       string
	vipsheader string
	nice       string // path to nice, empty when disabled
	timeout    time.Duration
	threads    int
	sem        chan struct{}
	version    [3]int
	keepFlag   []string // metadata stripping flags for savers
}

var _ Engine = (*VipsCLI)(nil)

const (
	defaultTimeout        = 30 * time.Second
	defaultMaxConcurrency = 2
)

var versionRe = regexp.MustCompile(`vips-(\d+)\.(\d+)\.(\d+)`)

// NewVipsCLI resolves the binaries, checks that they run and returns the
// engine. It fails when libvips is not installed.
func NewVipsCLI(cfg VipsConfig) (*VipsCLI, error) {
	e := &VipsCLI{timeout: cfg.Timeout}
	if e.timeout <= 0 {
		e.timeout = defaultTimeout
	}
	n := cfg.MaxConcurrency
	if n <= 0 {
		n = defaultMaxConcurrency
	}
	e.sem = make(chan struct{}, n)
	e.threads = max(1, cfg.ThreadsPerJob)

	var err error
	if e.vips, err = resolveBin(cfg.VipsPath, "vips"); err != nil {
		return nil, err
	}
	if e.vipsheader, err = resolveBin(cfg.VipsHeaderPath, "vipsheader"); err != nil {
		return nil, err
	}
	if cfg.Nice {
		// nice is optional: without it the engine still works, only at
		// normal priority.
		if p, err := exec.LookPath("nice"); err == nil {
			e.nice = p
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cpu int64
	out, err := e.run(ctx, &cpu, e.vips, "--version")
	if err != nil {
		return nil, fmt.Errorf("vips --version: %w", err)
	}
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return nil, fmt.Errorf("vips --version: unexpected output %q", firstLine(out))
	}
	for i := range 3 {
		e.version[i], _ = strconv.Atoi(m[i+1])
	}
	// `keep` replaced `strip` in libvips 8.15.
	if e.atLeast(8, 15) {
		e.keepFlag = []string{"--keep", "none"}
	} else {
		e.keepFlag = []string{"--strip"}
	}
	return e, nil
}

// Version returns the detected libvips version, e.g. "8.17.2".
func (e *VipsCLI) Version() string {
	return fmt.Sprintf("%d.%d.%d", e.version[0], e.version[1], e.version[2])
}

func (e *VipsCLI) atLeast(major, minor int) bool {
	return e.version[0] > major || (e.version[0] == major && e.version[1] >= minor)
}

func resolveBin(configured, name string) (string, error) {
	if configured == "" {
		configured = name
	}
	p, err := exec.LookPath(configured)
	if err != nil {
		return "", &Error{Code: CodeEngineFailure, Detail: name + " not found", Retryable: true, Err: err}
	}
	return p, nil
}

// acquire takes a concurrency slot or fails when ctx is done.
func (e *VipsCLI) acquire(ctx context.Context) error {
	select {
	case e.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return classifyCtx(ctx.Err())
	}
}

func (e *VipsCLI) release() { <-e.sem }

// childEnv is the environment of every child process: the parent's
// environment plus libvips settings that keep one job from using every
// core. With untrusted set, loaders libvips marks as unsafe for untrusted
// input are blocked (VIPS_BLOCK_UNTRUSTED); that list includes vipsload, so
// it is only set for calls that read the user-supplied source, not for our
// own intermediate .v files.
func childEnv(tmpDir string, untrusted bool, threads int) []string {
	env := os.Environ()
	env = append(env, "VIPS_CONCURRENCY="+strconv.Itoa(max(1, threads)), "VIPS_WARNING=0")
	if untrusted {
		env = append(env, "VIPS_BLOCK_UNTRUSTED=1")
	}
	if tmpDir != "" {
		env = append(env, "TMPDIR="+tmpDir)
	}
	return env
}

// cmdError is a failed child process.
type cmdError struct {
	argv   []string
	stderr string
	err    error
}

func (c *cmdError) Error() string {
	msg := strings.TrimSpace(c.stderr)
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return fmt.Sprintf("%s %s: %v: %s", filepath.Base(c.argv[0]), strings.Join(c.argv[1:2], " "), c.err, msg)
}

func (c *cmdError) Unwrap() error { return c.err }

// run executes bin with args (never through a shell) and returns stdout.
// The CPU time of the child is added to *cpuMillis.
func (e *VipsCLI) run(ctx context.Context, cpuMillis *int64, bin string, args ...string) (string, error) {
	return e.runIn(ctx, cpuMillis, "", true, bin, args...)
}

// runIn is run with a working/temp directory; untrusted selects the
// blocked-loader environment (see childEnv).
func (e *VipsCLI) runIn(ctx context.Context, cpuMillis *int64, tmpDir string, untrusted bool, bin string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", classifyCtx(err)
	}
	argv := append([]string{bin}, args...)
	if e.nice != "" {
		argv = append([]string{e.nice, "-n", "10"}, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = childEnv(tmpDir, untrusted, e.threads)
	cmd.Dir = tmpDir
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ps := cmd.ProcessState; ps != nil {
		atomic.AddInt64(cpuMillis, (ps.UserTime() + ps.SystemTime()).Milliseconds())
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", classifyCtx(ctx.Err())
		}
		return "", &cmdError{argv: argv[len(argv)-len(args)-1:], stderr: stderr.String(), err: err}
	}
	return stdout.String(), nil
}

func classifyCtx(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Detail: "image processing timed out", Retryable: false, Err: err}
	}
	return &Error{Code: CodeEngineFailure, Detail: "image processing cancelled", Retryable: true, Err: err}
}

// engineErr wraps an unexpected failure as a retryable engine_failure,
// passing classified errors through.
func engineErr(detail string, err error) error {
	if _, ok := AsError(err); ok {
		return err
	}
	return &Error{Code: CodeEngineFailure, Detail: detail, Retryable: true, Err: err}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// header is the subset of `vipsheader -a` output the engine uses.
type header struct {
	Width, Height int
	Bands         int
	Pages         int
	Orientation   int
	Loader        string
	Fields        map[string]string
}

// readHeader runs `vipsheader -a` on path. It reads only the header; no
// pixels are decoded. untrusted must be true for user-supplied files.
func (e *VipsCLI) readHeader(ctx context.Context, cpu *int64, path string, untrusted bool) (header, error) {
	out, err := e.runIn(ctx, cpu, "", untrusted, e.vipsheader, "-a", path)
	if err != nil {
		return header{}, err
	}
	return parseHeader(out), nil
}

// readVHeader reads width, height and bands from the fixed 64-byte header of
// a libvips native .v file, avoiding a vipsheader process for the engine's
// own intermediates.
func readVHeader(path string) (raster, error) {
	f, err := os.Open(path)
	if err != nil {
		return raster{}, err
	}
	defer f.Close()
	var b [16]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return raster{}, fmt.Errorf("read .v header: %w", err)
	}
	var bo binary.ByteOrder
	switch {
	case b[0] == 0xb6 && b[1] == 0xa6 && b[2] == 0xf2 && b[3] == 0x08:
		bo = binary.LittleEndian
	case b[0] == 0x08 && b[1] == 0xf2 && b[2] == 0xa6 && b[3] == 0xb6:
		bo = binary.BigEndian
	default:
		return raster{}, errors.New("not a vips .v file")
	}
	r := raster{
		Path:  path,
		W:     int(int32(bo.Uint32(b[4:8]))),
		H:     int(int32(bo.Uint32(b[8:12]))),
		Bands: int(int32(bo.Uint32(b[12:16]))),
	}
	if r.W <= 0 || r.H <= 0 || r.Bands <= 0 {
		return raster{}, errors.New("invalid .v header")
	}
	return r, nil
}

func parseHeader(out string) header {
	h := header{Pages: 1, Orientation: 1, Fields: map[string]string{}}
	for line := range strings.SplitSeq(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if _, dup := h.Fields[k]; !dup {
			h.Fields[k] = v
		}
		atoi := func() int { n, _ := strconv.Atoi(firstField(v)); return n }
		switch k {
		case "width":
			h.Width = atoi()
		case "height":
			h.Height = atoi()
		case "bands":
			h.Bands = atoi()
		case "n-pages":
			if n := atoi(); n > 0 {
				h.Pages = n
			}
		case "orientation":
			if n := atoi(); n >= 1 && n <= 8 {
				h.Orientation = n
			}
		case "vips-loader":
			h.Loader = v
		}
	}
	return h
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}
