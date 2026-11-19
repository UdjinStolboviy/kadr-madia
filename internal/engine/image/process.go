package image

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

// Process implements Engine (docs/PIPELINES.md §2):
//
//  1. `vips thumbnail`: decode the first page with shrink-on-load,
//     autorotate, convert to 8-bit sRGB, resize with lanczos3 (fit inside,
//     never enlarge) or smart-crop to the target aspect;
//  2. optional grayscale, normalise (1st–99th percentile stretch, computed
//     in Go from the raw raster), sharpen;
//  3. the result is the intermediate `.v` raster, decoded once;
//  4. the outputs are encoded concurrently from the intermediate, each with
//     the preset's quality strategy (budget | fixed | ssim); metadata is
//     stripped at save;
//  5. meanwhile ThumbHash and dominant color are computed in Go from the
//     intermediate's raw pixels.
//
// Child processes dominate the cost (docs/adr/0014), so steps that can be
// done in Go on the uncompressed .v raster are.
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

	p := newProc(e, ctx, tmp)
	in, _, err := safeInputPath(src.Path, tmp)
	if err != nil {
		return nil, engineErr("prepare source path", err)
	}
	inter, err := p.prepare(in, src, params)
	if err != nil {
		return nil, err
	}
	p.setInter(inter)
	if params.Quality.Mode == "ssim" {
		if p.ssimRef, err = p.grayFor(inter.Path); err != nil {
			return nil, err
		}
	}

	// Output names are fixed before the lanes start so they stay
	// deterministic.
	used := map[string]int{}
	names := make([]string, len(params.Outputs))
	bases := make([]string, len(params.Outputs))
	for i, o := range params.Outputs {
		base := o.Name
		if base == "" {
			base = "main"
		}
		fileName := base + "." + o.Format
		if n := used[fileName]; n > 0 {
			fileName = fmt.Sprintf("%s-%d.%s", base, n+1, o.Format)
		}
		used[base+"."+o.Format]++
		names[i], bases[i] = fileName, base
	}

	// One lane per output; the first failure cancels the others.
	laneCtx, cancelLanes := context.WithCancel(ctx)
	defer cancelLanes()
	p.ctx = laneCtx
	out := &Output{Renditions: make([]Rendition, len(params.Outputs))}
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() { firstErr = err; cancelLanes() })
	}
	for i, o := range params.Outputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := p.encodeOutput(o, params.Quality, filepath.Join(workDir, names[i]))
			if err != nil {
				fail(err)
				return
			}
			r.Name = bases[i]
			out.Renditions[i] = r
		}()
	}

	if params.Placeholder.ThumbHash || params.Placeholder.DominantColor {
		hash, color, err := p.placeholder()
		if err != nil {
			fail(err)
		} else {
			if params.Placeholder.ThumbHash {
				out.ThumbHash = hash
			}
			if params.Placeholder.DominantColor {
				out.DominantColor = color
			}
		}
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	out.CPUMillis = atomic.LoadInt64(&p.cpu)
	return out, nil
}

// proc holds the state of one Process call. Output lanes run concurrently;
// the fields below the mutex are shared between them.
type proc struct {
	e     *VipsCLI
	ctx   context.Context
	tmp   string
	cpu   int64 // atomic
	seq   atomic.Int64
	inter raster

	mu      sync.Mutex
	rasters map[int]*rasterEntry // long side → raster derived from inter
	ssimRef *grayPlane           // set before the lanes start (ssim mode)

	// Test seams; nil means the real vips calls.
	encodeHook func(o preset.ImageOutput, r raster, q, effort int) (string, int64, error)
	rasterHook func(side int) (raster, error)
}

type rasterEntry struct {
	once sync.Once
	r    raster
	err  error
}

func newProc(e *VipsCLI, ctx context.Context, tmp string) *proc {
	return &proc{e: e, ctx: ctx, tmp: tmp, rasters: map[int]*rasterEntry{}}
}

func (p *proc) setInter(r raster) {
	p.inter = r
	ent := &rasterEntry{r: r}
	ent.once.Do(func() {})
	p.rasters[max(r.W, r.H)] = ent
}

// raster is a decoded image file in the temp directory.
type raster struct {
	Path  string
	W, H  int
	Bands int
}

func (p *proc) tmpFile(ext string) string {
	return filepath.Join(p.tmp, fmt.Sprintf("t%03d%s", p.seq.Add(1), ext))
}

// vips runs a vips operation on files the engine wrote itself with the
// engine's ThreadsPerJob.
func (p *proc) vips(args ...string) (string, error) {
	return p.e.runIn(p.ctx, &p.cpu, p.tmp, false, 0, p.e.vips, args...)
}

// vipsN is vips with an explicit thread count.
func (p *proc) vipsN(threads int, args ...string) (string, error) {
	return p.e.runIn(p.ctx, &p.cpu, p.tmp, false, threads, p.e.vips, args...)
}

// vipsSource runs a vips operation that reads the user-supplied source,
// with untrusted loaders blocked.
func (p *proc) vipsSource(args ...string) (string, error) {
	return p.e.runIn(p.ctx, &p.cpu, p.tmp, true, 0, p.e.vips, args...)
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

	// Guarantee 8-bit sRGB regardless of the source (16-bit PNG, CMYK,
	// gray). `thumbnail --export-profile srgb` already produces it for every
	// input we have seen, so the extra process only runs when the header
	// says otherwise.
	cur := base
	h, err := readVFileHeader(base)
	if err != nil {
		return raster{}, engineErr("read header", err)
	}
	if !h.is8bitSRGB() {
		next := p.tmpFile(".v")
		if _, err := p.vips("colourspace", base, next, "srgb"); err != nil {
			return raster{}, engineErr("convert to sRGB", err)
		}
		cur = next
		if h, err = readVFileHeader(cur); err != nil {
			return raster{}, engineErr("read header", err)
		}
	}
	hasAlpha := h.Bands == 2 || h.Bands == 4

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
	if wm := params.Watermark; wm != nil && (strings.TrimSpace(wm.Text) != "" || strings.TrimSpace(wm.Image) != "") {
		next, err := p.applyWatermark(cur, wm)
		if err != nil {
			return raster{}, err
		}
		cur = next
	}
	r, err := readVHeader(cur)
	if err != nil {
		return raster{}, engineErr("read header", err)
	}
	return r, nil
}

// normalise stretches luminance so the 1st percentile maps to 0 and the
// 99th to 255. The same linear transform is applied to every color band;
// alpha is left untouched. The percentiles are computed in Go from the raw
// .v pixels (one `vips linear` process instead of four).
func (p *proc) normalise(cur string, gray, hasAlpha bool) (string, error) {
	img, err := readVImage(cur)
	if err != nil {
		return "", engineErr("normalise: read raster", err)
	}
	lo, hi := lumaPercentiles(img, gray, 1, 99)
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
// Results are shared between the output lanes (AVIF and WebP often settle
// on the same downscaled side); each side is produced once.
func (p *proc) rasterAt(side int) (raster, error) {
	if p.rasterHook != nil {
		return p.rasterHook(side)
	}
	long := max(p.inter.W, p.inter.H)
	if side >= long {
		return p.inter, nil
	}
	p.mu.Lock()
	ent, ok := p.rasters[side]
	if !ok {
		ent = &rasterEntry{}
		p.rasters[side] = ent
	}
	p.mu.Unlock()
	ent.once.Do(func() {
		path := p.tmpFile(".v")
		scale := float64(side) / float64(long)
		if _, err := p.vipsN(1, "resize", p.inter.Path, path, ftoa(scale), "--kernel", "lanczos3"); err != nil {
			ent.err = engineErr("resize", err)
			return
		}
		r, err := readVHeader(path)
		if err != nil {
			ent.err = engineErr("read header", err)
			return
		}
		ent.r = r
	})
	return ent.r, ent.err
}

// AVIF effort used for search encodes. At effort 0 an 800 px aom encode
// takes ≈ 70 ms of CPU against ≈ 600 ms at effort 4, and its size-vs-q
// curve has the same shape, ≈ 0–25 % larger (docs/adr/0014).
const avifSearchEffort = 0

// Ratios of final-effort (default 4) to search-effort AVIF size at the same
// q, measured on the fixtures at q 40–60: typically ≈ 0.6–0.85 (photos and
// documents ≈ 0.75–0.8), but ≥ 1 on pure noise. finalRatioTypical predicts
// the q step-up for the final encode; finalRatioMin decides when a qMin
// miss at search effort may still fit at final effort.
const (
	finalRatioTypical = 0.80
	finalRatioMin     = 0.75
	maxFinalStepUp    = 6
)

// finalEffort is the encoder effort for the delivered file.
func finalEffort(o preset.ImageOutput) int {
	if o.Format == FormatAVIF {
		if o.Effort <= 0 {
			return 4
		}
		return min(o.Effort, 9)
	}
	return min(o.Effort, 6) // WebP: 0 means the libwebp default
}

// searchEffort is the effort for search encodes: cheaper for AVIF, the
// final one for WebP (whose encodes are already cheap).
func searchEffort(o preset.ImageOutput) int {
	if o.Format == FormatAVIF {
		return min(avifSearchEffort, finalEffort(o))
	}
	return finalEffort(o)
}

// encode writes r with format o at quality q and effort to a temp file
// and returns its path and size. threads is VIPS_CONCURRENCY for the
// child (0 = ThreadsPerJob); libvips hands it to the AV1 encoder.
func (p *proc) encode(o preset.ImageOutput, r raster, q, effort, threads int) (string, int64, error) {
	if p.encodeHook != nil {
		return p.encodeHook(o, r, q, effort)
	}
	out := p.tmpFile("." + o.Format)
	var args []string
	switch o.Format {
	case FormatAVIF:
		args = []string{"heifsave", r.Path, out,
			"--Q", strconv.Itoa(q), "--compression", "av1", "--effort", strconv.Itoa(effort),
			"--subsample-mode", subsampleMode(o.Chroma)}
	case FormatWebP:
		args = []string{"webpsave", r.Path, out, "--Q", strconv.Itoa(q)}
		if o.SmartSubsample {
			args = append(args, "--smart-subsample")
		}
		if effort > 0 {
			args = append(args, "--effort", strconv.Itoa(effort))
		}
		threads = 1 // libwebp encodes on one thread
	}
	args = append(args, p.e.keepFlag...)
	if _, err := p.vipsN(threads, args...); err != nil {
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
	w, h, err := encodedDims(dst, o.Format)
	if err != nil {
		return Rendition{}, engineErr("read rendition dimensions", err)
	}
	return Rendition{
		Format:     o.Format,
		Path:       dst,
		Bytes:      res.bytes,
		Width:      w,
		Height:     h,
		Quality:    res.q,
		Iterations: res.iterations,
		OverBudget: res.overBudget,
	}, nil
}

type encResult struct {
	path       string
	bytes      int64
	q          int
	effort     int
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
func (p *proc) nearDeadline(lastEncode time.Duration) bool {
	dl, ok := p.ctx.Deadline()
	return ok && time.Until(dl) < 3*lastEncode+time.Second
}

func (p *proc) encodeFixed(o preset.ImageOutput, qp preset.Quality) (encResult, error) {
	q := qp.Q
	if q <= 0 {
		q = defaultFixedQ
	}
	path, n, err := p.encode(o, p.inter, clampQ(q), finalEffort(o), 0)
	if err != nil {
		return encResult{}, err
	}
	return encResult{path: path, bytes: n, q: clampQ(q), iterations: 1}, nil
}

func clampQ(q int) int { return max(1, min(100, q)) }

// encKey identifies an encode within one output's search.
type encKey struct{ side, q, effort int }

// encoder runs and memoises the encodes of one output, counting them.
type encoder struct {
	p          *proc
	o          preset.ImageOutput
	cache      map[encKey]encResult
	iterations int
}

func (en *encoder) at(r raster, side, q, effort, threads int) (encResult, error) {
	k := encKey{side, q, effort}
	if res, ok := en.cache[k]; ok {
		return res, nil
	}
	res, err := en.run(r, q, effort, threads)
	if err != nil {
		return encResult{}, err
	}
	en.iterations++
	if os.Getenv("KADR_DEBUG_ENC") != "" {
		fmt.Fprintf(os.Stderr, "DBG %s side=%d q=%d e=%d bytes=%d\n", en.o.Format, side, q, effort, res.bytes)
	}
	en.cache[k] = res
	return res, nil
}

func (en *encoder) run(r raster, q, effort, threads int) (encResult, error) {
	path, n, err := en.p.encode(en.o, r, q, effort, threads)
	if err != nil {
		return encResult{}, err
	}
	return encResult{path: path, bytes: n, q: q, effort: effort}, nil
}

// atBoth runs the final-effort and search-effort encodes at q
// concurrently (the speculative first step of a two-stage AVIF search).
func (en *encoder) atBoth(r raster, side, q, final, search int) (f, s encResult, err error) {
	var (
		wg   sync.WaitGroup
		sErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s, sErr = en.run(r, q, search, 1)
	}()
	f, err = en.run(r, q, final, 0)
	wg.Wait()
	if err == nil {
		err = sErr
	}
	if err != nil {
		return encResult{}, encResult{}, err
	}
	en.iterations += 2
	en.cache[encKey{side, q, final}] = f
	en.cache[encKey{side, q, search}] = s
	return f, s, nil
}

// encodeBudget implements ADR-0009 with the search of ADR-0014: find the
// highest quality in [qMin, qMax] (within searchTol) whose output fits
// maxBytes at the current size; when even qMin does not fit, move down the
// ladder side·downscaleStep^k to minSide. If the budget is unreachable the
// minSide/qMin result is returned with OverBudget set.
//
//   - The search (search.go) predicts q from a log-linear size model
//     instead of bisecting, starting at qMax.
//   - AVIF is searched at a cheap effort; the delivered file is one encode
//     at the preset effort with q stepped up by the expected size ratio,
//     verified against the budget (refineFinal).
//   - A ladder rung is skipped without an encode when even the
//     proportional-to-pixels estimate from the larger side (which
//     underestimates real sizes) is over budget at qMin.
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
	final, search := finalEffort(o), searchEffort(o)
	twoStage := final != search
	slope := slopeWebP
	if o.Format == FormatAVIF {
		slope = slopeAVIF
	}

	en := &encoder{p: p, o: o, cache: map[encKey]encResult{}}
	done := func(res encResult) (encResult, error) {
		res.iterations = en.iterations
		return res, nil
	}
	side, start := long, qMax
	if twoStage {
		// Speculate: most photos fit at qMax, and then the final-effort
		// encode is the answer. It runs alongside the search-effort probe
		// at qMax, so a miss costs CPU but no latency, and its size
		// calibrates the final step (refineFinal).
		f, _, err := en.atBoth(p.inter, side, qMax, final, search)
		if err != nil {
			return encResult{}, err
		}
		if f.bytes <= maxBytes {
			return done(f)
		}
	}
	for {
		r, err := p.rasterAt(side)
		if err != nil {
			return encResult{}, err
		}
		sr, err := searchQ(qMin, qMax, start, maxBytes, slope, func(q int) (int64, error) {
			res, err := en.at(r, side, q, search, 1)
			return res.bytes, err
		})
		if err != nil {
			return encResult{}, err
		}
		if sr.found(qMin) {
			best := en.cache[encKey{side, sr.fit, search}]
			if twoStage {
				if best, err = p.refineFinal(en, r, side, sr, qMax, maxBytes); err != nil {
					return encResult{}, err
				}
			}
			return done(best)
		}

		floor := en.cache[encKey{side, qMin, search}]
		searchFloorBytes := floor.bytes
		// The final effort is usually smaller: a near miss at qMin may fit.
		if twoStage && (float64(floor.bytes)*finalRatioMin <= float64(maxBytes) || side == minSide) {
			f, err := en.at(r, side, qMin, final, 0)
			if err != nil {
				return encResult{}, err
			}
			if f.bytes <= maxBytes {
				return done(f)
			}
			if f.bytes < floor.bytes {
				floor = f
			}
		}
		if side == minSide {
			// Unreachable budget: keep the smallest minSide/qMin result.
			floor.overBudget = true
			return done(floor)
		}

		// Next rung, skipping rungs that cannot fit even if size fell in
		// proportion to the pixel count.
		next := max(minSide, int(math.Floor(float64(side)*step)))
		area := func(s int) float64 { return float64(s) * float64(s) }
		for next > minSide && float64(searchFloorBytes)*area(next)/area(side) > float64(maxBytes) {
			next = max(minSide, int(math.Floor(float64(next)*step)))
		}
		// Start where the scaled curve of this side predicts a fit.
		scaled := float64(searchFloorBytes) * area(next) / area(side)
		start = qMax
		if q, ok := modelQ(sample{qMin, int64(scaled)}, curveSlope(sr.samples, slope), float64(maxBytes)*(1-searchAim)); ok {
			start = max(qMin, min(qMax, q))
		}
		side = next
	}
}

// refineFinal produces the delivered AVIF after a search at the cheap
// effort found q0 = sr.fit at this side. The final effort compresses
// better, so it first tries q0 stepped up by the q-equivalent of
// finalRatioTypical; when that misses it tries once more at the q the
// measured final-effort size predicts. If neither fits, the search-effort
// encode at q0 (which fits) is delivered. At most two final encodes.
func (p *proc) refineFinal(en *encoder, r raster, side int, sr searchResult, qMax int, maxBytes int64) (encResult, error) {
	final, search := finalEffort(en.o), searchEffort(en.o)
	q0 := sr.fit
	base := en.cache[encKey{side, q0, search}]
	sl := curveSlope(sr.samples, slopeAVIF)
	ratio := finalRatioTypical
	fTop, okF := en.cache[encKey{side, qMax, final}]
	sTop, okS := en.cache[encKey{side, qMax, search}]
	if okF && okS && sTop.bytes > 0 {
		// Measured at qMax by the speculative step.
		ratio = float64(fTop.bytes) / float64(sTop.bytes)
		if ratio >= 1 {
			return base, nil // final effort is not smaller here (noise-like content)
		}
	}
	stepUp := min(maxFinalStepUp, max(0, int(math.Floor(math.Log(1/ratio)/sl))))
	qF := min(qMax, q0+stepUp)
	f, err := en.at(r, side, qF, final, 0)
	if err != nil {
		return encResult{}, err
	}
	if f.bytes <= maxBytes {
		return f, nil
	}
	if q1, ok := modelQ(sample{qF, f.bytes}, sl, float64(maxBytes)*(1-searchAim)); ok {
		q1 = min(q1, qF-1)
		if q1 > q0 {
			f, err := en.at(r, side, q1, final, 0)
			if err != nil {
				return encResult{}, err
			}
			if f.bytes <= maxBytes {
				return f, nil
			}
		}
	}
	return base, nil
}

// encodeSSIM finds the lowest quality whose output reaches the SSIM target
// against the intermediate (see ssim.go). Quality is searched in
// [qMin, qMax] (defaults 30–85) by bisection to within ssimQTolerance; if
// even qMax misses the target, the qMax result is used. AVIF is searched
// at the cheap effort and delivered as one encode at the preset effort at
// the chosen q, verified against the target (one step up if it misses).
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
	final, search := finalEffort(o), searchEffort(o)
	en := &encoder{p: p, o: o, cache: map[encKey]encResult{}}
	side := max(p.inter.W, p.inter.H)
	var lastEncode time.Duration
	score := func(q, effort, threads int) (encResult, float64, error) {
		start := time.Now()
		res, err := en.at(p.inter, side, q, effort, threads)
		lastEncode = time.Since(start)
		if err != nil {
			return encResult{}, 0, err
		}
		cand, err := p.grayFor(res.path)
		if err != nil {
			return encResult{}, 0, err
		}
		return res, ssim(p.ssimRef, cand), nil
	}

	qTop := hi
	var best *encResult
	missedTop := false
	for lo <= hi {
		// Stop once the passing q is known within ssimQTolerance: finer
		// steps change bytes by a few percent but cost a full encode.
		if best != nil && best.q-lo < ssimQTolerance {
			break
		}
		// Deadline guard: keep the best passing result rather than fail
		// the job when another encode would likely overrun the timeout.
		if best != nil && p.nearDeadline(lastEncode) {
			break
		}
		q := (lo + hi) / 2
		res, s, err := score(q, search, 1)
		if err != nil {
			return encResult{}, err
		}
		if s >= target {
			best = &res
			hi = q - 1
		} else if q == qTop {
			best, missedTop = &res, true // fallback
			break
		} else {
			lo = q + 1
		}
	}
	if best == nil {
		res, _, err := score(qTop, search, 1)
		if err != nil {
			return encResult{}, err
		}
		best, missedTop = &res, true
	}
	out := *best
	if final != search && !p.nearDeadline(lastEncode) {
		f, s, err := score(best.q, final, 0)
		if err != nil {
			return encResult{}, err
		}
		out = f
		if s < target && !missedTop && best.q < qTop && !p.nearDeadline(lastEncode) {
			q2 := min(qTop, best.q+ssimQTolerance)
			if f2, _, err := score(q2, final, 0); err == nil {
				out = f2
			} else {
				return encResult{}, err
			}
		}
	}
	out.iterations = en.iterations
	return out, nil
}

// grayFor returns a downscaled luma plane of an image: the engine's own
// .v rasters are read directly, anything else is decoded by vips through a
// temporary PNG.
func (p *proc) grayFor(path string) (*grayPlane, error) {
	var img *rgbaPlane
	var err error
	if strings.HasSuffix(path, ".v") {
		img, err = readVImage(path)
	}
	if img == nil {
		if err != nil && !errors.Is(err, errVUnsupported) {
			return nil, engineErr("decode for ssim", err)
		}
		png := p.tmpFile(".png")
		if _, err := p.vipsN(1, "pngsave", path, png, "--compression", "1"); err != nil {
			return nil, engineErr("decode for ssim", err)
		}
		defer os.Remove(png)
		if img, err = decodeFile(png); err != nil {
			return nil, engineErr("decode for ssim", err)
		}
	}
	g := img.luma()
	return g.boxDown(ssimFactor(g.W, g.H)), nil
}

// placeholder computes the ThumbHash and dominant color from a ≤100 px
// version of the intermediate, area-averaged in Go from the raw .v pixels.
func (p *proc) placeholder() ([]byte, string, error) {
	img, err := readVImage(p.inter.Path)
	if errors.Is(err, errVUnsupported) {
		img, err = p.placeholderViaVips()
	}
	if err != nil {
		return nil, "", engineErr("placeholder raster", err)
	}
	img = downRGBA(img, 100)
	hash, err := rgbaToThumbHash(img.W, img.H, img.Pix)
	if err != nil {
		return nil, "", engineErr("thumbhash", err)
	}
	return hash, dominantColor(img), nil
}

func (p *proc) placeholderViaVips() (*rgbaPlane, error) {
	png := p.tmpFile(".png")
	if _, err := p.vipsN(1, "pngsave", p.inter.Path, png); err != nil {
		return nil, err
	}
	return decodeFile(png)
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
