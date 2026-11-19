// Package delivery serves private renditions over signed URLs from Kadr
// itself: the "origin" alternative to the kadr-edge Worker (docs/adr/0017).
// The listener (KADR_DELIVERY_ADDR) is published through a Cloudflare Tunnel
// and verifies the same URLs the Worker does:
//
//	https://media.example.com/{key}?exp=&kid=&sig=[&download=]   one object
//	https://media.example.com/{dir}/master.m3u8?exp=&kid=&sig=&prefix=1   HLS package
//
// Unlike the Worker, one listener serves every project: the kid names the
// project, the project's storage profile names the bucket, and a key outside
// the project's own prefix is refused even with a valid signature.
package delivery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/VictoriaMetrics/metrics"

	"github.com/UdjinStolboviy/kadr-madia/internal/storage"
)

// ErrUnknownKey is returned by a Resolver for a kid that does not exist or
// is no longer accepted (retired).
var ErrUnknownKey = errors.New("delivery: unknown signing key")

// Grant is what a valid kid gives access to.
type Grant struct {
	Secret []byte
	// Prefix every served key must start with ("{project_slug}/").
	Prefix string
	Driver storage.Driver
	Bucket string
}

// Resolver looks up a signing key (pipeline.Service implements it).
type Resolver interface {
	DeliveryGrant(ctx context.Context, kid string) (*Grant, error)
}

// Limits and cache lifetimes.
const (
	// MaxBrowserCache caps Cache-Control max-age (further capped by the URL's validity).
	MaxBrowserCache = time.Hour
	// grantTTL is how long a resolved kid is cached: a retired key stops
	// working within this time.
	grantTTL       = 30 * time.Second
	missTTL        = 10 * time.Second
	maxGrants      = 4096
	maxPlaylist    = 4 << 20
	chunkSize      = 256 << 10
	chunkDeadline  = 60 * time.Second
	defaultStreams = 64
)

// Config configures the handler.
type Config struct {
	Resolver Resolver
	Logger   *slog.Logger
	Metrics  *metrics.Set
	// MaxStreams bounds concurrent responses (default 64); beyond it the
	// handler answers 503 so delivery cannot starve the media workers.
	MaxStreams int
	// CDNMaxAge, when > 0, lets Cloudflare cache responses for up to this
	// long (Cloudflare-CDN-Cache-Control). Safe only while the zone's cache
	// key includes the full query string (the default): see docs/OPERATIONS.md.
	CDNMaxAge time.Duration
	Now       func() time.Time
}

// Handler serves signed private objects.
type Handler struct {
	cfg  Config
	sem  chan struct{}
	mu   sync.Mutex
	keys map[string]cachedGrant

	reqs  func(status int)
	bytes *metrics.Counter
}

type cachedGrant struct {
	g       *Grant
	expires time.Time
}

// New returns the delivery handler.
func New(cfg Config) *Handler {
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = defaultStreams
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &Handler{cfg: cfg, sem: make(chan struct{}, cfg.MaxStreams), keys: map[string]cachedGrant{}, reqs: func(int) {}}
	if cfg.Metrics != nil {
		h.reqs = func(status int) {
			cfg.Metrics.GetOrCreateCounter(fmt.Sprintf(`kadr_delivery_requests_total{status="%d"}`, status)).Inc()
		}
		h.bytes = cfg.Metrics.GetOrCreateCounter("kadr_delivery_bytes_total")
	}
	return h
}

var baseHeaders = map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"}

func (h *Handler) text(w http.ResponseWriter, status int, msg string) {
	for k, v := range baseHeaders {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
	h.reqs(status)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.text(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		w.Header().Set("Retry-After", "5")
		h.text(w, http.StatusServiceUnavailable, "busy")
		return
	}
	req, grant, err := h.verify(r)
	if err != nil {
		if !errors.Is(err, errForbidden) {
			h.cfg.Logger.Error("delivery: resolve signing key", "err", err)
			h.text(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		h.text(w, http.StatusForbidden, "forbidden") // the reason is deliberately not disclosed
		return
	}
	if req.prefix && strings.HasSuffix(req.key, ".m3u8") {
		h.servePlaylist(w, r, req, grant)
		return
	}
	h.serveObject(w, r, req, grant)
}

// ───────────── signature ─────────────

var errForbidden = errors.New("forbidden")

type signedRequest struct {
	key      string
	exp      int64
	download string
	hasDL    bool
	prefix   bool
	query    url.Values
}

var expRe = regexp.MustCompile(`^\d{1,15}$`)

// CanonicalString is GET\n/{key}\n{exp}\n{download}: identical to
// pipeline.SignKey, sdk/node and kadr-edge.
func CanonicalString(key string, exp int64, download string) string {
	return "GET\n/" + key + "\n" + strconv.FormatInt(exp, 10) + "\n" + download
}

// CanonicalPrefixString is GETP\n/{dir}\n{exp}\n (dir ends with "/").
func CanonicalPrefixString(dir string, exp int64) string {
	return "GETP\n/" + dir + "\n" + strconv.FormatInt(exp, 10) + "\n"
}

// KeyFromPath decodes the storage key of a request path, refusing empty,
// NUL and dot segments.
func KeyFromPath(escaped string) (string, bool) {
	parts := strings.Split(strings.TrimLeft(escaped, "/"), "/")
	for i, p := range parts {
		d, err := url.PathUnescape(p)
		if err != nil || d == "." || d == ".." || strings.ContainsRune(d, 0) {
			return "", false
		}
		parts[i] = d
	}
	key := strings.TrimLeft(strings.Join(parts, "/"), "/")
	return key, key != ""
}

func (h *Handler) verify(r *http.Request) (*signedRequest, *Grant, error) {
	q := r.URL.Query()
	expRaw, kid, sig := q.Get("exp"), q.Get("kid"), q.Get("sig")
	if !q.Has("exp") || !q.Has("kid") || !q.Has("sig") || !expRe.MatchString(expRaw) || kid == "" {
		return nil, nil, errForbidden
	}
	exp, _ := strconv.ParseInt(expRaw, 10, 64)
	sr := &signedRequest{exp: exp, prefix: q.Get("prefix") == "1", query: q}
	if dl := q.Get("download"); !sr.prefix && dl != "" { // a directory signature does not cover a name
		sr.download, sr.hasDL = dl, true
	}
	key, ok := KeyFromPath(r.URL.EscapedPath())
	if !ok {
		return nil, nil, errForbidden
	}
	sr.key = key
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return nil, nil, errForbidden
	}
	g, err := h.grant(r.Context(), kid)
	if err != nil {
		return nil, nil, err
	}
	m := hmac.New(sha256.New, g.Secret)
	if sr.prefix {
		m.Write([]byte(CanonicalPrefixString(key[:strings.LastIndex(key, "/")+1], exp)))
	} else {
		m.Write([]byte(CanonicalString(key, exp, sr.download)))
	}
	if !hmac.Equal(m.Sum(nil), got) {
		return nil, nil, errForbidden
	}
	if h.cfg.Now().Unix() > exp {
		return nil, nil, errForbidden
	}
	// A project's key reaches only the project's own objects (and never
	// retained originals, which are not served).
	if g.Prefix == "" || !strings.HasPrefix(key, g.Prefix) {
		return nil, nil, errForbidden
	}
	return sr, g, nil
}

// grant resolves a kid through a small cache (misses are cached too, so
// random kids do not reach the database on every request).
func (h *Handler) grant(ctx context.Context, kid string) (*Grant, error) {
	now := h.cfg.Now()
	h.mu.Lock()
	if c, ok := h.keys[kid]; ok && now.Before(c.expires) {
		h.mu.Unlock()
		if c.g == nil {
			return nil, errForbidden
		}
		return c.g, nil
	}
	h.mu.Unlock()
	g, err := h.cfg.Resolver.DeliveryGrant(ctx, kid)
	if err != nil && !errors.Is(err, ErrUnknownKey) {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.keys) >= maxGrants {
		for k, c := range h.keys {
			if !now.Before(c.expires) {
				delete(h.keys, k)
			}
		}
		if len(h.keys) >= maxGrants {
			clear(h.keys)
		}
	}
	if err != nil {
		h.keys[kid] = cachedGrant{expires: now.Add(missTTL)}
		return nil, errForbidden
	}
	h.keys[kid] = cachedGrant{g: g, expires: now.Add(grantTTL)}
	return g, nil
}

// ───────────── objects ─────────────

func (h *Handler) headers(w http.ResponseWriter, info storage.ObjectInfo, sr *signedRequest) {
	hd := w.Header()
	for k, v := range baseHeaders {
		hd.Set(k, v)
	}
	ct := info.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	hd.Set("Content-Type", ct)
	if info.ETag != "" {
		hd.Set("ETag", `"`+info.ETag+`"`)
	}
	if !info.LastModified.IsZero() {
		hd.Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	}
	hd.Set("Accept-Ranges", "bytes")
	ttl := h.remaining(sr.exp, MaxBrowserCache)
	hd.Set("Cache-Control", "private, max-age="+strconv.Itoa(ttl))
	if h.cfg.CDNMaxAge > 0 {
		hd.Set("Cloudflare-CDN-Cache-Control", "max-age="+strconv.Itoa(h.remaining(sr.exp, h.cfg.CDNMaxAge)))
	}
	// A per-request download name wins; otherwise the disposition stored with
	// the object (documents are stored as attachments).
	if sr.hasDL {
		hd.Set("Content-Disposition", ContentDisposition(sr.download))
	} else if info.ContentDisposition != "" {
		hd.Set("Content-Disposition", info.ContentDisposition)
	}
}

// remaining is the URL's validity in seconds, capped at limit.
func (h *Handler) remaining(exp int64, limit time.Duration) int {
	left := exp - h.cfg.Now().Unix()
	return int(min(max(0, left), int64(limit/time.Second)))
}

func (h *Handler) serveObject(w http.ResponseWriter, r *http.Request, sr *signedRequest, g *Grant) {
	ctx := r.Context()
	isHead := r.Method == http.MethodHead
	rangeHdr := r.Header.Get("Range")
	inm := r.Header.Get("If-None-Match")

	// Plain GET: one storage request.
	if !isHead && rangeHdr == "" && inm == "" {
		body, info, err := g.Driver.Get(ctx, g.Bucket, sr.key)
		if err != nil {
			h.storageError(w, err)
			return
		}
		defer body.Close()
		h.headers(w, info, sr)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		h.stream(w, http.StatusOK, body)
		return
	}

	info, err := g.Driver.Head(ctx, g.Bucket, sr.key)
	if err != nil {
		h.storageError(w, err)
		return
	}
	h.headers(w, info, sr)
	if etagMatches(inm, info.ETag) {
		w.WriteHeader(http.StatusNotModified)
		h.reqs(http.StatusNotModified)
		return
	}
	rg := parseRange(rangeHdr, info.Size)
	if ifRangeRejects(r.Header.Get("If-Range"), info) {
		rg = rangeResult{kind: rangeNone}
	}
	switch rg.kind {
	case rangeUnsatisfiable:
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(info.Size, 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		h.reqs(http.StatusRequestedRangeNotSatisfiable)
		return
	case rangeOK:
		n := rg.end - rg.start + 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rg.start, rg.end, info.Size))
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
		if isHead {
			w.WriteHeader(http.StatusPartialContent)
			h.reqs(http.StatusPartialContent)
			return
		}
		body, _, err := g.Driver.GetRange(ctx, g.Bucket, sr.key, rg.start, n)
		if err != nil {
			h.clearObjectHeaders(w)
			h.storageError(w, err)
			return
		}
		defer body.Close()
		h.stream(w, http.StatusPartialContent, body)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if isHead {
		w.WriteHeader(http.StatusOK)
		h.reqs(http.StatusOK)
		return
	}
	body, _, err := g.Driver.Get(ctx, g.Bucket, sr.key)
	if err != nil {
		h.clearObjectHeaders(w)
		h.storageError(w, err)
		return
	}
	defer body.Close()
	h.stream(w, http.StatusOK, body)
}

func (h *Handler) clearObjectHeaders(w http.ResponseWriter) {
	for _, k := range []string{"ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "Content-Length", "Content-Disposition", "Cloudflare-CDN-Cache-Control"} {
		w.Header().Del(k)
	}
}

func (h *Handler) storageError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		h.text(w, http.StatusNotFound, "not found")
		return
	}
	h.cfg.Logger.Error("delivery: storage read failed", "err", err)
	h.text(w, http.StatusBadGateway, "storage unavailable")
}

// stream copies body to w, extending the write deadline per chunk so a slow
// client is cut off only when it stalls, not when a large video takes long.
func (h *Handler) stream(w http.ResponseWriter, status int, body io.Reader) {
	rc := http.NewResponseController(w)
	w.WriteHeader(status)
	h.reqs(status)
	buf := make([]byte, chunkSize)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(chunkDeadline))
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if h.bytes != nil {
				h.bytes.Add(n)
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				h.cfg.Logger.Warn("delivery: storage stream interrupted", "err", rerr)
			}
			return
		}
	}
}

// ───────────── HLS playlists ─────────────

func (h *Handler) servePlaylist(w http.ResponseWriter, r *http.Request, sr *signedRequest, g *Grant) {
	body, info, err := g.Driver.Get(r.Context(), g.Bucket, sr.key)
	if err != nil {
		h.storageError(w, err)
		return
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, maxPlaylist))
	if err != nil {
		h.storageError(w, err)
		return
	}
	q := url.Values{}
	for _, k := range []string{"exp", "kid", "sig", "prefix"} {
		q.Set(k, sr.query.Get(k))
	}
	out := RewritePlaylist(string(raw), q.Encode())
	hd := w.Header()
	for k, v := range baseHeaders {
		hd.Set(k, v)
	}
	ct := info.ContentType
	if ct == "" {
		ct = "application/vnd.apple.mpegurl"
	}
	hd.Set("Content-Type", ct)
	// Rewritten per request: never cached by the CDN.
	hd.Set("Cache-Control", "private, max-age="+strconv.Itoa(h.remaining(sr.exp, time.Minute)))
	hd.Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	h.reqs(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, out)
	}
}

var schemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
var uriAttrRe = regexp.MustCompile(`URI="([^"]*)"`)

// RewritePlaylist appends query to every relative URI of a playlist, so
// players that resolve segment URLs without the query string stay signed.
func RewritePlaylist(body, query string) string {
	add := func(u string) string {
		if u == "" || strings.HasPrefix(u, "/") || schemeRe.MatchString(u) {
			return u
		}
		if strings.Contains(u, "?") {
			return u + "&" + query
		}
		return u + "?" + query
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		t := strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(t, "#"):
			t = uriAttrRe.ReplaceAllStringFunc(t, func(m string) string {
				return `URI="` + add(uriAttrRe.FindStringSubmatch(m)[1]) + `"`
			})
		case t != "":
			t = add(t)
		}
		lines[i] = t
	}
	return strings.Join(lines, "\n")
}

// ───────────── HTTP helpers ─────────────

// ContentDisposition is an RFC 6266 / 5987 attachment header with an ASCII fallback.
func ContentDisposition(name string) string {
	var clean strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
		case r == '/' || r == '\\':
			clean.WriteRune('_')
		default:
			clean.WriteRune(r)
		}
	}
	c := clean.String()
	var ascii strings.Builder
	for _, r := range c {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			ascii.WriteRune('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	star := strings.NewReplacer("'", "%27", "(", "%28", ")", "%29", "*", "%2A").Replace(url.PathEscape(c))
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + star
}

func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	strip := func(s string) string { return strings.Trim(strings.TrimPrefix(strings.TrimSpace(s), "W/"), `"`) }
	for _, t := range strings.Split(header, ",") {
		if strip(t) == etag {
			return true
		}
	}
	return false
}

func ifRangeRejects(v string, info storage.ObjectInfo) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "W/") {
		return strings.Trim(v, `"`) != info.ETag
	}
	t, err := http.ParseTime(v)
	return err != nil || !t.Equal(info.LastModified.UTC().Truncate(time.Second))
}

type rangeKind int

const (
	rangeNone rangeKind = iota
	rangeUnsatisfiable
	rangeOK
)

type rangeResult struct {
	kind       rangeKind
	start, end int64 // inclusive
}

var rangeRe = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// parseRange handles a single "bytes=" range; multi-range or invalid syntax
// is ignored (full response), like kadr-edge.
func parseRange(header string, size int64) rangeResult {
	m := rangeRe.FindStringSubmatch(strings.TrimSpace(header))
	if m == nil || (m[1] == "" && m[2] == "") {
		return rangeResult{}
	}
	if m[1] == "" {
		n, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return rangeResult{}
		}
		if n == 0 || size == 0 {
			return rangeResult{kind: rangeUnsatisfiable}
		}
		return rangeResult{kind: rangeOK, start: max(0, size-n), end: size - 1}
	}
	start, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return rangeResult{}
	}
	end := size - 1
	if m[2] != "" {
		if end, err = strconv.ParseInt(m[2], 10, 64); err != nil || end < start {
			return rangeResult{}
		}
	}
	if start >= size {
		return rangeResult{kind: rangeUnsatisfiable}
	}
	return rangeResult{kind: rangeOK, start: start, end: min(end, size-1)}
}
