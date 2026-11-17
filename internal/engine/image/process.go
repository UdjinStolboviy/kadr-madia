package image

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Process implements Engine (docs/PIPELINES.md §2):
//
//  1. `vips thumbnail`: decode the first page with shrink-on-load,
//     autorotate, convert to sRGB, resize with lanczos3 (fit inside, never
//     enlarge) or smart-crop to the target aspect;
//  2. optional grayscale, normalise (1st–99th percentile stretch), sharpen;
//  3. the result is the intermediate `.v` raster, decoded once;
//  4. every output is encoded from the intermediate with the preset's
//     quality strategy (budget | fixed | ssim); metadata is stripped at save;
//  5. ThumbHash and dominant color come from a ≤100 px PNG of the
//     intermediate, decoded in Go.
//
// All temporary files live in a hidden directory inside workDir that is
// removed before returning; only the final renditions remain.
func (e *VipsCLI) Process(ctx context.Context, src SourceInfo, params preset.ImageParams, workDir string) (*Output, error) {
	if len(params.Outputs) == 0 {
		return nil, &Error{Code: CodeEngineFailure, Detail: "preset has no outputs"}
	}
	for _, o := range params.Outputs {
		if o.Format != FormatAVIF && o.Format != FormatWebP {
			return nil, &Error{Code: CodeEngineFailure, Detail: fmt.Sprintf("unsupported output format %q", o.Format)}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	if err := e.acquire(ctx); err != nil {
		return nil, err
	}
	defer e.release()

	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, engineErr("resolve work dir", err)
	}
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return nil, engineErr("create work dir", err)
	}
	tmp, err := os.MkdirTemp(workDir, ".vips-")
	if err != nil {
		return nil, engineErr("create temp dir", err)
	}
	defer os.RemoveAll(tmp)

	p := &proc{e: e, ctx: ctx, tmp: tmp, rasters: map[int]raster{}}
	in, _, err := safeInputPath(src.Path, tmp)
	if err != nil {
		return nil, engineErr("prepare source path", err)
	}
	inter, err := p.prepare(in, src, params)
	if err != nil {
		return nil, err
	}
	p.inter = inter
	p.rasters[max(inter.W, inter.H)] = inter

	out := &Output{}
	used := map[string]int{}
	for _, o := range params.Outputs {
		base := o.Name
		if base == "" {
			base = "main"
		}
		fileName := base + "." + o.Format
		if n := used[fileName]; n > 0 {
			fileName = fmt.Sprintf("%s-%d.%s", base, n+1, o.Format)
		}
		used[base+"."+o.Format]++
		r, err := p.encodeOutput(o, params.Quality, filepath.Join(workDir, fileName))
		if err != nil {
			return nil, err
		}
		r.Name = base
		out.Renditions = append(out.Renditions, r)
	}

	if params.Placeholder.ThumbHash || params.Placeholder.DominantColor {
		hash, color, err := p.placeholder()
		if err != nil {
			return nil, err
		}
		if params.Placeholder.ThumbHash {
			out.ThumbHash = hash
		}
		if params.Placeholder.DominantColor {
			out.DominantColor = color
		}
	}
	out.CPUMillis = p.cpu
	return out, nil
}

// proc holds the state of one Process call.
type proc struct {
	e       *VipsCLI
	ctx     context.Context
	tmp     string
	cpu     int64
	seq     int
	inter   raster
	rasters map[int]raster // long side → raster derived from inter
	ssimRef *grayPlane     // lazily computed reference for ssim mode

	// Test seams for the budget search; nil means the real vips calls.
	lastEncode time.Duration // duration of the latest ssim-mode encode

	encodeHook func(o preset.ImageOutput, r raster, q int) (string, int64, error)
	rasterHook func(side int) (raster, error)
}

// raster is a decoded image file in the temp directory.
type raster struct {
	Path  string
	W, H  int
	Bands int
}

func (p *proc) tmpFile(ext string) string {
	p.seq++
	return filepath.Join(p.tmp, fmt.Sprintf("t%03d%s", p.seq, ext))
}

// vips runs a vips operation on files the engine wrote itself.
func (p *proc) vips(args ...string) (string, error) {
	return p.e.runIn(p.ctx, &p.cpu, p.tmp, false, p.e.vips, args...)
}

// vipsSource runs a vips operation that reads the user-supplied source,
// with untrusted loaders blocked.
func (p *proc) vipsSource(args ...string) (string, error) {
	return p.e.runIn(p.ctx, &p.cpu, p.tmp, true, p.e.vips, args...)
}

func (p *proc) header(path string) (raster, error) {
	if strings.HasSuffix(path, ".v") {
		r, err := readVHeader(path)
		if err != nil {
			return raster{}, engineErr("read header", err)
		}
		return r, nil
	}
	h, err := p.e.readHeader(p.ctx, &p.cpu, path, false)
	if err != nil {
		return raster{}, engineErr("read header", err)
	}
	return raster{Path: path, W: h.Width, H: h.Height, Bands: h.Bands}, nil
}

// prepare runs steps 1–3 and returns the intermediate raster.
func (p *proc) prepare(in string, src SourceInfo, params preset.ImageParams) (raster, error) {
	rz := params.Resize
	maxW, maxH := rz.MaxWidth, rz.MaxHeight
	if maxW <= 0 {
		maxW = max(src.Width, 1)
	}
	if maxH <= 0 {
		maxH = max(src.Height, 1)
	}
	crop := ""
	if rz.Crop != nil {
		crop = strings.ToLower(*rz.Crop)
	}
	if crop == "center" {
		crop = "centre"
	}
	var aspect float64
	if rz.Aspect != nil && *rz.Aspect != "" {
		a, err := parseAspect(*rz.Aspect)
		if err != nil {
			return raster{}, &Error{Code: CodeEngineFailure, Detail: err.Error()}
		}
		aspect = a
	}

	base := p.tmpFile(".v")
	args := []string{"thumbnail", in, base}
	if rz.Fit == "cover" || (crop != "" && aspect > 0) {
		if crop == "" {
			crop = "centre"
		}
		if aspect == 0 {
			aspect = float64(maxW) / float64(maxH)
		}
		bw, bh := cropBox(maxW, maxH, src.Width, src.Height, aspect)
		args = append(args, strconv.Itoa(bw), "--height", strconv.Itoa(bh), "--crop", crop)
	} else {
		args = append(args, strconv.Itoa(maxW), "--height", strconv.Itoa(maxH))
	}
	args = append(args, "--size", "down", "--export-profile", "srgb")
	if p.e.atLeast(8, 12) {
		args = append(args, "--fail-on", "truncated")
	}
	if _, err := p.vipsSource(args...); err != nil {
		if _, ok := AsError(err); ok {
			return raster{}, err
		}
		return raster{}, &Error{Code: CodeCorrupt, Detail: "cannot decode image", Err: err}
	}

	// Guarantee 8-bit sRGB regardless of the source (16-bit PNG, CMYK, gray).
	cur := p.tmpFile(".v")
	if _, err := p.vips("colourspace", base, cur, "srgb"); err != nil {
		return raster{}, engineErr("convert to sRGB", err)
	}
	info, err := p.header(cur)
	if err != nil {
		return raster{}, err
	}
	hasAlpha := info.Bands == 2 || info.Bands == 4

	if params.Color.Grayscale {
		next := p.tmpFile(".v")
		if _, err := p.vips("colourspace", cur, next, "b-w"); err != nil {
			return raster{}, engineErr("grayscale", err)
		}
		cur = next
	}
	if params.Color.Normalise {
		next, err := p.normalise(cur, params.Color.Grayscale, hasAlpha)
		if err != nil {
			return raster{}, err
		}
		cur = next
	}
	if s := params.Sharpen; s != nil && s.Sigma > 0 {
		next := p.tmpFile(".v")
		if _, err := p.vips("sharpen", cur, next,
			"--sigma", ftoa(s.Sigma), "--m1", ftoa(s.M1), "--m2", ftoa(s.M2)); err != nil {
			return raster{}, engineErr("sharpen", err)
		}
		cur = next
	}
	return p.header(cur)
}

// normalise stretches luminance so the 1st percentile maps to 0 and the
// 99th to 255. The same linear transform is applied to every color band;
// alpha is left untouched.
func (p *proc) normalise(cur string, gray, hasAlpha bool) (string, error) {
	lum := cur
	if !gray {
		lum = p.tmpFile(".v")
		if _, err := p.vips("colourspace", cur, lum, "b-w"); err != nil {
			return "", engineErr("normalise: luminance", err)
		}
	}
	band := p.tmpFile(".v")
	if _, err := p.vips("extract_band", lum, band, "0"); err != nil {
		return "", engineErr("normalise: extract band", err)
	}
	percent := func(pc string) (float64, error) {
		out, err := p.vips("percent", band, pc)
		if err != nil {
			return 0, engineErr("normalise: percent", err)
		}
		return strconv.ParseFloat(strings.TrimSpace(out), 64)
	}
	lo, err := percent("1")
	if err != nil {
		return "", err
	}
	hi, err := percent("99")
	if err != nil {
		return "", err
	}
	if hi-lo < 1 || (lo <= 0 && hi >= 255) {
		return cur, nil // flat image or already full range
	}
	a := 255 / (hi - lo)
	b := -lo * a
	colorBands := 3
	if gray {
		colorBands = 1
	}
	as, bs := make([]string, 0, 4), make([]string, 0, 4)
	for range colorBands {
		as, bs = append(as, ftoa(a)), append(bs, ftoa(b))
	}
	if hasAlpha {
		as, bs = append(as, "1"), append(bs, "0")
	}
	next := p.tmpFile(".v")
	// "--" ends option parsing: the offset is usually negative.
	if _, err := p.vips("linear", "--uchar", "--", cur, next, strings.Join(as, " "), strings.Join(bs, " ")); err != nil {
		return "", engineErr("normalise: linear", err)
	}
	return next, nil
}

// cropBox returns the largest w×h box with the given aspect ratio that fits
// both the preset bounds and the source (so nothing is enlarged).
func cropBox(maxW, maxH, srcW, srcH int, aspect float64) (int, int) {
	bw := float64(min(maxW, srcW))
	bh := float64(min(maxH, srcH))
	if bw/bh > aspect {
		bw = bh * aspect
	} else {
		bh = bw / aspect
	}
	return max(1, int(math.Round(bw))), max(1, int(math.Round(bh)))
}

func parseAspect(s string) (float64, error) {
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return 0, fmt.Errorf("invalid aspect %q (want W:H)", s)
	}
	w, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, fmt.Errorf("invalid aspect %q (want W:H)", s)
	}
	return w / h, nil
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// rasterAt returns the intermediate resized so its long side is side.
// Results are cached so AVIF and WebP share downscaled rasters.
func (p *proc) rasterAt(side int) (raster, error) {
	if p.rasterHook != nil {
		return p.rasterHook(side)
	}
	if r, ok := p.rasters[side]; ok {
		return r, nil
	}
	long := max(p.inter.W, p.inter.H)
	if side >= long {
		return p.inter, nil
	}
	path := p.tmpFile(".v")
	scale := float64(side) / float64(long)
	if _, err := p.vips("resize", p.inter.Path, path, ftoa(scale), "--kernel", "lanczos3"); err != nil {
		return raster{}, engineErr("resize", err)
	}
	r, err := p.header(path)
	if err != nil {
		return raster{}, err
	}
	p.rasters[side] = r
	return r, nil
}

// encode writes r with format o at quality q to a temp file and returns its
// path and size.
func (p *proc) encode(o preset.ImageOutput, r raster, q int) (string, int64, error) {
	if p.encodeHook != nil {
		return p.encodeHook(o, r, q)
	}
	out := p.tmpFile("." + o.Format)
	var args []string
	switch o.Format {
	case FormatAVIF:
		effort := o.Effort
		if effort <= 0 {
			effort = 4
		}
		args = []string{"heifsave", r.Path, out,
			"--Q", strconv.Itoa(q), "--compression", "av1", "--effort", strconv.Itoa(min(effort, 9)),
			"--subsample-mode", subsampleMode(o.Chroma)}
	case FormatWebP:
		args = []string{"webpsave", r.Path, out, "--Q", strconv.Itoa(q)}
		if o.SmartSubsample {
			args = append(args, "--smart-subsample")
		}
		if o.Effort > 0 {
			args = append(args, "--effort", strconv.Itoa(min(o.Effort, 6)))
		}
	}
	args = append(args, p.e.keepFlag...)
	if _, err := p.vips(args...); err != nil {
		return "", 0, engineErr("encode "+o.Format, err)
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", 0, engineErr("stat encoded file", err)
	}
	return out, st.Size(), nil
}

// subsampleMode maps the preset chroma setting to heifsave subsample-mode.
func subsampleMode(chroma string) string {
	switch chroma {
	case "4:2:0":
		return "on"
	case "4:4:4":
		return "off"
	}
	return "auto"
}

// encodeOutput runs the quality strategy for one output and moves the
// chosen file to dst.
func (p *proc) encodeOutput(o preset.ImageOutput, qp preset.Quality, dst string) (Rendition, error) {
	var (
		res encResult
		err error
	)
	switch qp.Mode {
	case "budget":
		if qp.MaxBytes <= 0 {
			res, err = p.encodeFixed(o, qp)
		} else {
			res, err = p.encodeBudget(o, qp)
		}
	case "ssim":
		res, err = p.encodeSSIM(o, qp)
	case "fixed", "":
		res, err = p.encodeFixed(o, qp)
	default:
		return Rendition{}, &Error{Code: CodeEngineFailure, Detail: fmt.Sprintf("unknown quality mode %q", qp.Mode)}
	}
	if err != nil {
		return Rendition{}, err
	}
	if err := os.Rename(res.path, dst); err != nil {
		return Rendition{}, engineErr("move rendition", err)
	}
	// Dimensions are read back from the encoded file, not assumed.
	info, err := p.header(dst)
	if err != nil {
		return Rendition{}, err
	}
	return Rendition{
		Format:     o.Format,
		Path:       dst,
		Bytes:      res.bytes,
		Width:      info.W,
		Height:     info.H,
		Quality:    res.q,
		Iterations: res.iterations,
		OverBudget: res.overBudget,
	}, nil
}

type encResult struct {
	path       string
	bytes      int64
	q          int
	iterations int
	overBudget bool
}

const (
	defaultFixedQ  = 52
	defaultQMin    = 30
	defaultQMax    = 60
	defaultStep    = 0.85
	defaultSSIMQHi = 85
	defaultSSIM    = 0.985
	ssimQTolerance = 4
)

// nearDeadline reports whether fewer than three encodes' worth of time
// remain before the Process timeout.
func (p *proc) nearDeadline() bool {
	dl, ok := p.ctx.Deadline()
	return ok && time.Until(dl) < 3*p.lastEncode+time.Second
}

func (p *proc) encodeFixed(o preset.ImageOutput, qp preset.Quality) (encResult, error) {
	q := qp.Q
	if q <= 0 {
		q = defaultFixedQ
	}
	path, n, err := p.encode(o, p.inter, clampQ(q))
	if err != nil {
		return encResult{}, err
	}
	return encResult{path: path, bytes: n, q: clampQ(q), iterations: 1}, nil
}

func clampQ(q int) int { return max(1, min(100, q)) }

// encodeBudget implements ADR-0009: binary-search the highest quality in
// [qMin, qMax] whose output fits maxBytes at the current size; when even
// qMin does not fit, shrink the long side by downscaleStep and repeat, down
// to minSide. If the budget is unreachable the minSide/qMin result is
// returned with OverBudget set.
//
// Two refinements over the pseudo-code in docs/PIPELINES.md, both assuming
// (as the binary search already does) that size grows with quality:
//   - on downscaled sides qMin is tried first, so a side that cannot fit
//     costs one encode instead of a full search;
//   - the last step searches at exactly minSide instead of encoding
//     minSide/qMin blindly, so a fitting result at minSide is still found.
func (p *proc) encodeBudget(o preset.ImageOutput, qp preset.Quality) (encResult, error) {
	qMin, qMax := qp.QMin, qp.QMax
	if qMin <= 0 {
		qMin = defaultQMin
	}
	if qMax <= 0 {
		qMax = defaultQMax
	}
	qMin, qMax = clampQ(qMin), clampQ(qMax)
	if qMin > qMax {
		qMin, qMax = qMax, qMin
	}
	step := qp.DownscaleStep
	if step <= 0 || step >= 1 {
		step = defaultStep
	}
	step = max(step, 0.5)
	maxBytes := int64(qp.MaxBytes)
	long := max(p.inter.W, p.inter.H)
	minSide := qp.MinSide
	if minSide <= 0 || minSide > long {
		minSide = long
	}

	iterations := 0
	side := long
	for {
		r, err := p.rasterAt(side)
		if err != nil {
			return encResult{}, err
		}
		var best, floor *encResult // floor: qMin result that did not fit
		try := func(q int) (bool, error) {
			path, n, err := p.encode(o, r, q)
			if err != nil {
				return false, err
			}
			iterations++
			res := &encResult{path: path, bytes: n, q: q}
			if n <= maxBytes {
				if best != nil {
					_ = os.Remove(best.path)
				}
				best = res
				return true, nil
			}
			if q == qMin {
				floor = res
			} else {
				_ = os.Remove(path)
			}
			return false, nil
		}
		lo, hi := qMin, qMax
		if side != long {
			ok, err := try(qMin)
			if err != nil {
				return encResult{}, err
			}
			if ok {
				lo = qMin + 1
			} else {
				hi = qMin - 1 // skip the search
			}
		}
		for lo <= hi {
			q := (lo + hi) / 2
			ok, err := try(q)
			if err != nil {
				return encResult{}, err
			}
			if ok {
				lo = q + 1
			} else {
				hi = q - 1
			}
		}
		if best != nil {
			if floor != nil {
				_ = os.Remove(floor.path)
			}
			best.iterations = iterations
			return *best, nil
		}
		if side == minSide {
			// Unreachable budget: keep the minSide/qMin result.
			floor.iterations = iterations
			floor.overBudget = true
			return *floor, nil
		}
		_ = os.Remove(floor.path)
		side = max(minSide, int(math.Floor(float64(side)*step)))
	}
}

// encodeSSIM finds the lowest quality whose output reaches the SSIM target
// against the intermediate (see ssim.go). Quality is searched in
// [qMin, qMax] (defaults 30–85); if even qMax misses the target, the qMax
// result is used.
func (p *proc) encodeSSIM(o preset.ImageOutput, qp preset.Quality) (encResult, error) {
	target := qp.Target
	if target <= 0 || target >= 1 {
		target = defaultSSIM
	}
	lo, hi := qp.QMin, qp.QMax
	if lo <= 0 {
		lo = defaultQMin
	}
	if hi <= 0 {
		hi = defaultSSIMQHi
	}
	lo, hi = clampQ(lo), clampQ(hi)
	if lo > hi {
		lo, hi = hi, lo
	}
	if p.ssimRef == nil {
		ref, err := p.grayFor(p.inter.Path)
		if err != nil {
			return encResult{}, err
		}
		p.ssimRef = ref
	}
	qTop := hi
	var best *encResult
	iterations := 0
	for lo <= hi {
		// Stop once the passing q is known within ssimQTolerance: finer
		// steps change bytes by a few percent but cost a full encode.
		if best != nil && best.q-lo < ssimQTolerance {
			break
		}
		// Deadline guard: keep the best passing result rather than fail
		// the job when another encode would likely overrun the timeout.
		if best != nil && p.nearDeadline() {
			break
		}
		q := (lo + hi) / 2
		start := time.Now()
		path, n, err := p.encode(o, p.inter, q)
		p.lastEncode = time.Since(start)
		if err != nil {
			return encResult{}, err
		}
		iterations++
		cand, err := p.grayFor(path)
		if err != nil {
			return encResult{}, err
		}
		if ssim(p.ssimRef, cand) >= target {
			if best != nil {
				_ = os.Remove(best.path)
			}
			best = &encResult{path: path, bytes: n, q: q}
			hi = q - 1
		} else if q == qTop {
			best = &encResult{path: path, bytes: n, q: q} // fallback, kept
			break
		} else {
			_ = os.Remove(path)
			lo = q + 1
		}
	}
	if best == nil {
		path, n, err := p.encode(o, p.inter, qTop)
		if err != nil {
			return encResult{}, err
		}
		iterations++
		best = &encResult{path: path, bytes: n, q: qTop}
	}
	best.iterations = iterations
	return *best, nil
}

// grayFor decodes any image vips can read into a downscaled luma plane via a
// temporary PNG.
func (p *proc) grayFor(path string) (*grayPlane, error) {
	png := p.tmpFile(".png")
	if _, err := p.vips("pngsave", path, png, "--compression", "1"); err != nil {
		return nil, engineErr("decode for ssim", err)
	}
	defer os.Remove(png)
	img, err := decodeFile(png)
	if err != nil {
		return nil, engineErr("decode for ssim", err)
	}
	g := img.luma()
	return g.boxDown(ssimFactor(g.W, g.H)), nil
}

// placeholder computes the ThumbHash and dominant color from a ≤100 px
// version of the intermediate.
func (p *proc) placeholder() ([]byte, string, error) {
	long := max(p.inter.W, p.inter.H)
	png := p.tmpFile(".png")
	var err error
	if long > 100 {
		_, err = p.vips("resize", p.inter.Path, png, ftoa(100/float64(long)), "--kernel", "lanczos3")
	} else {
		_, err = p.vips("pngsave", p.inter.Path, png)
	}
	if err != nil {
		return nil, "", engineErr("placeholder raster", err)
	}
	img, err := decodeFile(png)
	if err != nil {
		return nil, "", engineErr("placeholder decode", err)
	}
	if img.W > 100 || img.H > 100 { // rounding guard
		return nil, "", engineErr("placeholder raster", fmt.Errorf("unexpected size %dx%d", img.W, img.H))
	}
	hash, err := rgbaToThumbHash(img.W, img.H, img.Pix)
	if err != nil {
		return nil, "", engineErr("thumbhash", err)
	}
	return hash, dominantColor(img), nil
}

// dominantColor is the alpha-weighted mean color of the placeholder raster,
// averaged in linear light so it is not darkened by gamma, formatted as
// "#rrggbb". Fully transparent images yield "#000000".
func dominantColor(img *rgbaPlane) string {
	var r, g, b, wsum float64
	for i := 0; i < len(img.Pix); i += 4 {
		a := float64(img.Pix[i+3]) / 255
		if a == 0 {
			continue
		}
		r += srgbToLinear[img.Pix[i]] * a
		g += srgbToLinear[img.Pix[i+1]] * a
		b += srgbToLinear[img.Pix[i+2]] * a
		wsum += a
	}
	if wsum == 0 {
		return "#000000"
	}
	return fmt.Sprintf("#%02x%02x%02x", linearToSRGB(r/wsum), linearToSRGB(g/wsum), linearToSRGB(b/wsum))
}

var srgbToLinear = func() (t [256]float64) {
	for i := range t {
		c := float64(i) / 255
		if c <= 0.04045 {
			t[i] = c / 12.92
		} else {
			t[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return t
}()

func linearToSRGB(v float64) uint8 {
	var c float64
	if v <= 0.0031308 {
		c = v * 12.92
	} else {
		c = 1.055*math.Pow(v, 1/2.4) - 0.055
	}
	return uint8(max(0, min(255, math.Round(c*255))))
}
