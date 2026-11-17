package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Golden tests run the real libvips CLI against test/fixtures/images. They
// are skipped when libvips is not installed (and in -short mode), so CI
// without vips still passes.

var fixturesDir = filepath.Join("..", "..", "..", "test", "fixtures", "images")

func newTestEngine(tb testing.TB) *VipsCLI {
	tb.Helper()
	if testing.Short() {
		tb.Skip("golden tests skipped in -short mode")
	}
	// KADR_VIPS_THREADS sets ThreadsPerJob, e.g. to compare benchmarks.
	threads, _ := strconv.Atoi(os.Getenv("KADR_VIPS_THREADS"))
	e, err := NewVipsCLI(VipsConfig{MaxConcurrency: runtime.NumCPU(), Timeout: 2 * time.Minute, ThreadsPerJob: threads})
	if err != nil {
		tb.Skipf("libvips not available: %v", err)
	}
	return e
}

func fixture(tb testing.TB, name string) string {
	tb.Helper()
	p, err := filepath.Abs(filepath.Join(fixturesDir, name))
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		tb.Skipf("fixture %s missing (run test/fixtures/images/generate.sh): %v", name, err)
	}
	return p
}

type namedPreset struct {
	Slug   string
	Params preset.ImageParams
}

func imagePresets(tb testing.TB) []namedPreset {
	tb.Helper()
	var out []namedPreset
	for _, d := range preset.DefaultPresets() {
		if d.Kind != "image" {
			continue
		}
		var p preset.ImageParams
		if err := json.Unmarshal(d.Params, &p); err != nil {
			tb.Fatal(err)
		}
		out = append(out, namedPreset{d.Slug, p})
	}
	return out
}

// goldenFixtures are the processable fixtures.
var goldenFixtures = []string{
	"photo.jpg", "texture.jpg", "screenshot.png", "document.jpg",
	"alpha.png", "rotated.jpg", "sample.heic", "animated.gif",
}

// ssimFloor is the minimum luma SSIM of a rendition against the source
// downscaled to the same size. texture.jpg is synthetic noise that cannot
// fit a 20 KB budget without visible loss, so it only has to stay
// recognisable. doc-lite is compared after the same grayscale conversion;
// normalise changes contrast, which SSIM penalises, hence a lower floor.
//
// photo-balanced targets SSIM 0.985 as measured by the engine (at ≤1024 px,
// see ssim.go); measured here at full rendition size the same files score
// lower, so the floor is 0.90.
func ssimFloor(presetSlug, fixtureName string) float64 {
	if fixtureName == "texture.jpg" {
		return 0.30
	}
	switch presetSlug {
	case "photo-balanced":
		return 0.90
	case "doc-lite":
		if fixtureName == "document.jpg" || fixtureName == "screenshot.png" {
			return 0.80
		}
		return 0.70 // photographic input, contrast changed by normalise
	case "avatar":
		return 0.85
	}
	return 0.88
}

type goldenRow struct {
	fixture, preset string
	r               Rendition
	ssim            float64
	ms              int64
}

func TestGoldenPresets(t *testing.T) {
	e := newTestEngine(t)
	presets := imagePresets(t)
	var (
		mu   sync.Mutex
		rows []goldenRow
	)
	t.Run("group", func(t *testing.T) {
		for _, np := range presets {
			for _, fx := range goldenFixtures {
				t.Run(np.Slug+"/"+fx, func(t *testing.T) {
					t.Parallel()
					path := fixture(t, fx)
					before := fileSum(t, path)
					src, err := e.Probe(context.Background(), path, np.Params.Limits)
					if err != nil {
						t.Fatalf("Probe: %v", err)
					}
					dir := t.TempDir()
					start := time.Now()
					out, err := e.Process(context.Background(), src, np.Params, dir)
					if err != nil {
						t.Fatalf("Process: %v", err)
					}
					ms := time.Since(start).Milliseconds()
					if after := fileSum(t, path); after != before {
						t.Fatal("source file was modified")
					}
					checkWorkDir(t, dir, out)
					if len(out.Renditions) != len(np.Params.Outputs) {
						t.Fatalf("got %d renditions, want %d", len(out.Renditions), len(np.Params.Outputs))
					}
					for i, r := range out.Renditions {
						want := np.Params.Outputs[i]
						if r.Format != want.Format || r.Name != want.Name {
							t.Errorf("rendition %d is %s/%s, want %s/%s", i, r.Name, r.Format, want.Name, want.Format)
						}
						checkRendition(t, e, np, src, fx, r)
						s := renditionSSIM(t, e, np, src, r)
						if floor := ssimFloor(np.Slug, fx); s < floor {
							t.Errorf("%s: SSIM %.4f below floor %.2f", r.Format, s, floor)
						}
						mu.Lock()
						rows = append(rows, goldenRow{fx, np.Slug, r, s, ms})
						mu.Unlock()
					}
					checkPlaceholder(t, np.Params.Placeholder, out)
					if out.CPUMillis <= 0 {
						t.Errorf("CPUMillis = %d, want > 0", out.CPUMillis)
					}
				})
			}
		}
	})
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.fixture != b.fixture {
			return a.fixture < b.fixture
		}
		if a.preset != b.preset {
			return a.preset < b.preset
		}
		return a.r.Format < b.r.Format
	})
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n| fixture | preset | fmt | bytes | dims | q | iter | over | ssim | ms (job) |\n|---|---|---|---:|---|---:|---:|---|---:|---:|\n")
	for _, r := range rows {
		over := ""
		if r.r.OverBudget {
			over = "yes"
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %d | %dx%d | %d | %d | %s | %.4f | %d |\n",
			r.fixture, r.preset, r.r.Format, r.r.Bytes, r.r.Width, r.r.Height, r.r.Quality, r.r.Iterations, over, r.ssim, r.ms)
	}
	t.Log(sb.String())
}

func fileSum(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// checkWorkDir asserts that only the renditions remain in workDir.
func checkWorkDir(t *testing.T, dir string, out *Output) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, r := range out.Renditions {
		if filepath.Dir(r.Path) != dir {
			t.Errorf("rendition %s outside workDir", r.Path)
		}
		want = append(want, filepath.Base(r.Path))
	}
	for _, en := range entries {
		if !slices.Contains(want, en.Name()) {
			t.Errorf("leftover file in workDir: %s", en.Name())
		}
	}
}

func checkRendition(t *testing.T, e *VipsCLI, np namedPreset, src SourceInfo, fx string, r Rendition) {
	t.Helper()
	p := np.Params
	st, err := os.Stat(r.Path)
	if err != nil {
		t.Fatalf("%s: %v", r.Format, err)
	}
	if st.Size() != r.Bytes {
		t.Errorf("%s: Bytes=%d but file has %d", r.Format, r.Bytes, st.Size())
	}
	head := make([]byte, 64)
	f, _ := os.Open(r.Path)
	n, _ := f.Read(head)
	f.Close()
	if got := sniffFormat(head[:n]); got != r.Format {
		t.Errorf("file sniffs as %q, want %q", got, r.Format)
	}
	if p.Quality.Mode == "budget" && !r.OverBudget && r.Bytes > int64(p.Quality.MaxBytes) {
		t.Errorf("%s: %d bytes over budget %d without OverBudget", r.Format, r.Bytes, p.Quality.MaxBytes)
	}
	if r.Width > p.Resize.MaxWidth || r.Height > p.Resize.MaxHeight {
		t.Errorf("%s: %dx%d exceeds %dx%d", r.Format, r.Width, r.Height, p.Resize.MaxWidth, p.Resize.MaxHeight)
	}
	if r.Width > src.Width || r.Height > src.Height {
		t.Errorf("%s: %dx%d enlarges source %dx%d", r.Format, r.Width, r.Height, src.Width, src.Height)
	}
	if p.Resize.Aspect != nil {
		if d := r.Width - r.Height; d < -1 || d > 1 {
			t.Errorf("%s: %dx%d is not 1:1", r.Format, r.Width, r.Height)
		}
	} else {
		srcAR := float64(src.Width) / float64(src.Height)
		ar := float64(r.Width) / float64(r.Height)
		if d := ar/srcAR - 1; d < -0.02 || d > 0.02 {
			t.Errorf("%s: aspect %.3f differs from source %.3f", r.Format, ar, srcAR)
		}
	}
	// No metadata: neither libvips nor a raw byte scan finds EXIF/XMP.
	h, err := e.readHeader(context.Background(), new(int64), r.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"exif-data", "xmp-data", "iptc-data"} {
		if _, ok := h.Fields[k]; ok {
			t.Errorf("%s: output carries %s", r.Format, k)
		}
	}
	if h.Orientation != 1 {
		t.Errorf("%s: output orientation tag %d", r.Format, h.Orientation)
	}
	raw, _ := os.ReadFile(r.Path)
	if bytes.Contains(raw, []byte("Exif\x00\x00")) || bytes.Contains(raw, []byte("<x:xmpmeta")) {
		t.Errorf("%s: EXIF/XMP bytes found in output", r.Format)
	}
	if fx == "rotated.jpg" {
		checkOrientation(t, e, np, r)
	}
}

// checkOrientation verifies rotated.jpg (stored landscape, Orientation=6,
// red block in the stored top-left) came out portrait with the red block in
// the top-right corner.
func checkOrientation(t *testing.T, e *VipsCLI, np namedPreset, r Rendition) {
	t.Helper()
	if np.Params.Resize.Aspect != nil {
		return // square crop
	}
	if r.Height <= r.Width {
		t.Errorf("%s: %dx%d is not portrait; EXIF orientation not applied", r.Format, r.Width, r.Height)
		return
	}
	img := decodeViaVips(t, e, r.Path)
	region := func(x0, y0, x1, y1 float64) (rr, gg, bb float64) {
		var n float64
		for y := int(y0 * float64(img.H)); y < int(y1*float64(img.H)); y++ {
			for x := int(x0 * float64(img.W)); x < int(x1*float64(img.W)); x++ {
				i := (y*img.W + x) * 4
				rr += float64(img.Pix[i])
				gg += float64(img.Pix[i+1])
				bb += float64(img.Pix[i+2])
				n++
			}
		}
		return rr / n, gg / n, bb / n
	}
	// Red block occupies stored x<240,y<200 of 1200x800 → displayed
	// top-right: x > 600/800, y < 240/1200.
	tr, tg, tb := region(0.80, 0.02, 0.98, 0.15)
	lr, lg, lb := region(0.02, 0.02, 0.20, 0.15)
	if np.Params.Color.Grayscale {
		// The block's luma differs clearly from the gradient corner it
		// would occupy if rotation were skipped or reversed.
		if d := (tr + tg + tb - lr - lg - lb) / 3; d > -30 && d < 30 {
			t.Errorf("%s: grayscale orientation check failed: top-right %.0f vs top-left %.0f", r.Format, tr, lr)
		}
		return
	}
	if !(tr > 150 && tg < 90 && tb < 90) {
		t.Errorf("%s: top-right is rgb(%.0f,%.0f,%.0f), want red", r.Format, tr, tg, tb)
	}
	if lr > 150 && lg < 90 {
		t.Errorf("%s: top-left is red; rotation went the wrong way", r.Format)
	}
}

func decodeViaVips(t *testing.T, e *VipsCLI, path string) *rgbaPlane {
	t.Helper()
	png := filepath.Join(t.TempDir(), "decoded.png")
	if out, err := exec.Command(e.vips, "pngsave", path, png).CombinedOutput(); err != nil {
		t.Fatalf("decode %s: %v: %s", path, err, out)
	}
	img, err := decodeFile(png)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// renditionSSIM compares the rendition with the source run through the same
// geometry (thumbnail to the preset box, same crop), resampled to the
// rendition size, in luma.
func renditionSSIM(t *testing.T, e *VipsCLI, np namedPreset, src SourceInfo, r Rendition) float64 {
	t.Helper()
	rz := np.Params.Resize
	ref := filepath.Join(t.TempDir(), "ref.png")
	args := []string{"thumbnail", src.Path, ref}
	if rz.Aspect != nil {
		a, _ := parseAspect(*rz.Aspect)
		bw, bh := cropBox(rz.MaxWidth, rz.MaxHeight, src.Width, src.Height, a)
		crop := "centre"
		if rz.Crop != nil {
			crop = *rz.Crop
		}
		args = append(args, strconv.Itoa(bw), "--height", strconv.Itoa(bh), "--crop", crop)
	} else {
		args = append(args, strconv.Itoa(rz.MaxWidth), "--height", strconv.Itoa(rz.MaxHeight))
	}
	args = append(args, "--size", "down")
	if out, err := exec.Command(e.vips, args...).CombinedOutput(); err != nil {
		t.Fatalf("reference: %v: %s", err, out)
	}
	refImg, err := decodeFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeViaVips(t, e, r.Path).luma()
	want := refImg.luma().resample(got.W, got.H)
	return ssim(want, got)
}

func checkPlaceholder(t *testing.T, ph preset.Placeholder, out *Output) {
	t.Helper()
	if ph.ThumbHash {
		if len(out.ThumbHash) < 5 || len(out.ThumbHash) > 25 {
			t.Errorf("ThumbHash length %d", len(out.ThumbHash))
		}
	} else if out.ThumbHash != nil {
		t.Error("ThumbHash set but not requested")
	}
	if ph.DominantColor {
		c := out.DominantColor
		if len(c) != 7 || c[0] != '#' {
			t.Errorf("DominantColor = %q", c)
		} else if _, err := strconv.ParseUint(c[1:], 16, 32); err != nil {
			t.Errorf("DominantColor = %q", c)
		}
	} else if out.DominantColor != "" {
		t.Error("DominantColor set but not requested")
	}
}

func TestProbe(t *testing.T) {
	e := newTestEngine(t)
	def := imagePresets(t)[0].Params.Limits
	cases := []struct {
		name, file string
		limits     func(preset.ImageLimits) preset.ImageLimits
		wantCode   string
		check      func(*testing.T, SourceInfo)
	}{
		{name: "jpeg", file: "photo.jpg", check: func(t *testing.T, s SourceInfo) {
			if s.Format != "jpeg" || s.ContentType != "image/jpeg" || s.Width != 1600 || s.Height != 1200 || s.Pages != 1 {
				t.Errorf("got %+v", s)
			}
		}},
		{name: "exif-rotated swaps dims", file: "rotated.jpg", check: func(t *testing.T, s SourceInfo) {
			if s.Width != 800 || s.Height != 1200 {
				t.Errorf("got %dx%d, want 800x1200", s.Width, s.Height)
			}
		}},
		{name: "png alpha", file: "alpha.png", check: func(t *testing.T, s SourceInfo) {
			if s.Format != "png" || s.Width != 800 {
				t.Errorf("got %+v", s)
			}
		}},
		{name: "animated gif pages", file: "animated.gif", check: func(t *testing.T, s SourceInfo) {
			if s.Format != "gif" || s.Pages < 2 || s.Width != 320 || s.Height != 240 {
				t.Errorf("got %+v", s)
			}
		}},
		{name: "heic", file: "sample.heic", check: func(t *testing.T, s SourceInfo) {
			if s.Format != "heic" || s.ContentType != "image/heic" || s.Width != 1024 {
				t.Errorf("got %+v", s)
			}
		}},
		{name: "unsupported text", file: "not-an-image.txt", wantCode: CodeUnsupportedMedia},
		{name: "not accepted", file: "photo.jpg", wantCode: CodeUnsupportedMedia,
			limits: func(l preset.ImageLimits) preset.ImageLimits { l.Accept = []string{"png"}; return l }},
		{name: "too many pixels", file: "photo.jpg", wantCode: CodeTooManyPixels,
			limits: func(l preset.ImageLimits) preset.ImageLimits { l.MaxInputPixels = 1000; return l }},
		{name: "too large", file: "photo.jpg", wantCode: CodeTooLarge,
			limits: func(l preset.ImageLimits) preset.ImageLimits { l.MaxInputBytes = 100; return l }},
		{name: "corrupt truncated jpeg", file: "corrupt.jpg", wantCode: CodeCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := def
			if c.limits != nil {
				l = c.limits(l)
			}
			s, err := e.Probe(context.Background(), fixture(t, c.file), l)
			if c.wantCode != "" {
				ee, ok := AsError(err)
				if !ok || ee.Code != c.wantCode {
					t.Fatalf("err = %v, want code %s", err, c.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, s)
		})
	}
}

func TestProcessCorruptWithoutProbe(t *testing.T) {
	e := newTestEngine(t)
	p := imagePresets(t)[0].Params
	src := SourceInfo{Path: fixture(t, "corrupt.jpg"), Format: "jpeg", Width: 1600, Height: 1200, Pages: 1}
	_, err := e.Process(context.Background(), src, p, t.TempDir())
	if ee, ok := AsError(err); !ok || ee.Code != CodeCorrupt {
		t.Fatalf("err = %v, want corrupt", err)
	}
}

func TestProcessTimeout(t *testing.T) {
	e := newTestEngine(t)
	e.timeout = time.Millisecond
	p := imagePresets(t)[0].Params
	src := SourceInfo{Path: fixture(t, "photo.jpg"), Format: "jpeg", Width: 1600, Height: 1200, Pages: 1}
	_, err := e.Process(context.Background(), src, p, t.TempDir())
	if ee, ok := AsError(err); !ok || ee.Code != CodeTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestProcessFixedAndCentreCrop(t *testing.T) {
	e := newTestEngine(t)
	path := fixture(t, "photo.jpg")
	p := imagePresets(t)[0].Params
	p.Quality = preset.Quality{Mode: "fixed", Q: 50}
	p.Resize = preset.Resize{MaxWidth: 640, MaxHeight: 640, Fit: "cover", Aspect: ptrTo("16:9"), Crop: ptrTo("centre")}
	p.Placeholder = preset.Placeholder{}
	src, err := e.Probe(context.Background(), path, p.Limits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := e.Process(context.Background(), src, p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Renditions {
		if r.Quality != 50 || r.Iterations != 1 || r.Width != 640 || r.Height != 360 {
			t.Errorf("%s: %+v", r.Format, r)
		}
	}
	if out.ThumbHash != nil || out.DominantColor != "" {
		t.Error("placeholder produced but not requested")
	}
}

func ptrTo[T any](v T) *T { return &v }

// BenchmarkPresets reports the time of one Process call per preset on
// photo.jpg. Run: go test -run x -bench Presets ./internal/engine/image
func BenchmarkPresets(b *testing.B) {
	e := newTestEngine(b)
	path := fixture(b, "photo.jpg")
	for _, np := range imagePresets(b) {
		b.Run(np.Slug, func(b *testing.B) {
			src, err := e.Probe(context.Background(), path, np.Params.Limits)
			if err != nil {
				b.Fatal(err)
			}
			var cpu int64
			for b.Loop() {
				dir := b.TempDir()
				out, err := e.Process(context.Background(), src, np.Params, dir)
				if err != nil {
					b.Fatal(err)
				}
				cpu += out.CPUMillis
			}
			b.ReportMetric(float64(cpu)/float64(b.N), "cpu-ms/op")
		})
	}
}
