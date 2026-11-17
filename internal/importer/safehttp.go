// Package importer fetches media from remote sources for import jobs:
// SSRF-safe HTTP(S) downloads and cloud drives via rclone.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Errors classify download failures so the pipeline can decide on retries.
var (
	ErrBlockedAddress = errors.New("importer: address is not allowed")
	ErrBadURL         = errors.New("importer: invalid URL")
	ErrTooLarge       = errors.New("importer: remote file is too large")
	ErrTooManyRedirs  = errors.New("importer: too many redirects")
)

// StatusError is a non-2xx response. 4xx (except 408/429) are permanent.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("importer: remote answered HTTP %d", e.Code) }

// Permanent reports whether retrying cannot help.
func (e *StatusError) Permanent() bool {
	return e.Code >= 400 && e.Code < 500 && e.Code != http.StatusRequestTimeout && e.Code != http.StatusTooManyRequests
}

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24",
	"192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// IsBlocked reports whether ip is private, loopback, link-local, CGNAT,
// metadata, multicast, reserved or an IPv4-mapped form of those.
func IsBlocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Fetcher downloads URLs safely: http/https only, no credentials, the host is
// resolved once, every resolved address is checked and the connection is made
// to the verified IP (no DNS rebinding); redirects are limited and re-checked.
type Fetcher struct {
	// Resolver overrides DNS (tests).
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
	// AllowAddr, when set, can permit otherwise blocked addresses (tests only).
	AllowAddr    func(netip.Addr) bool
	AllowAnyPort bool          // tests only
	MaxRedirects int           // default 5
	Timeout      time.Duration // whole download, default 10 min
	UserAgent    string
}

// ValidateURL checks syntax and policy without touching the network.
func ValidateURL(raw string) (*url.URL, error) { return validateURL(raw, false) }

func validateURL(raw string, anyPort bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, ErrBadURL
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL are not allowed", ErrBadURL)
	}
	if len(raw) > 2048 {
		return nil, fmt.Errorf("%w: too long", ErrBadURL)
	}
	if p := u.Port(); !anyPort {
		switch p {
		case "", "80", "443", "8080", "8443":
		default:
			return nil, fmt.Errorf("%w: port %s is not allowed", ErrBadURL, p)
		}
	}
	return u, nil
}

func (f *Fetcher) dialer() *http.Transport {
	res := f.Resolver
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []netip.Addr
			if ip, perr := netip.ParseAddr(host); perr == nil {
				ips = []netip.Addr{ip}
			} else if res != nil {
				if ips, err = res.LookupNetIP(ctx, "ip", host); err != nil {
					return nil, err
				}
			} else {
				if ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host); err != nil {
					return nil, err
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("importer: %s did not resolve", host)
			}
			for _, ip := range ips { // every address must be allowed, not just the one we dial
				if IsBlocked(ip) && (f.AllowAddr == nil || !f.AllowAddr(ip)) {
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, ip)
				}
			}
			d := net.Dialer{Timeout: 15 * time.Second}
			var last error
			for _, ip := range ips {
				c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          0,
		DisableKeepAlives:     true,
	}
}

// Get downloads raw into w, enforcing maxBytes. It returns the byte count,
// the final Content-Type header and the file name hint from the URL path.
func (f *Fetcher) Get(ctx context.Context, raw string, maxBytes int64, w io.Writer) (n int64, contentType, name string, err error) {
	u, err := validateURL(raw, f.AllowAnyPort)
	if err != nil {
		return 0, "", "", err
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxRedirs := f.MaxRedirects
	if maxRedirs <= 0 {
		maxRedirs = 5
	}
	tr := f.dialer()
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirs {
				return ErrTooManyRedirs
			}
			_, err := validateURL(req.URL.String(), f.AllowAnyPort) // scheme/credentials/port re-checked; IPs at dial time
			return err
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, "", "", ErrBadURL
	}
	ua := f.UserAgent
	if ua == "" {
		ua = "kadr-importer/1"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "image/*,video/*,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, "", "", &StatusError{Code: resp.StatusCode}
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return 0, "", "", ErrTooLarge
	}
	var body io.Reader = resp.Body
	if maxBytes > 0 {
		body = io.LimitReader(resp.Body, maxBytes+1)
	}
	n, err = io.Copy(w, body)
	if err != nil {
		return n, "", "", err
	}
	if maxBytes > 0 && n > maxBytes {
		return n, "", "", ErrTooLarge
	}
	name = resp.Request.URL.Path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return n, resp.Header.Get("Content-Type"), name, nil
}

// Validate checks a URL against the fetcher's policy without network access.
func (f *Fetcher) Validate(raw string) error {
	_, err := validateURL(raw, f.AllowAnyPort)
	return err
}
