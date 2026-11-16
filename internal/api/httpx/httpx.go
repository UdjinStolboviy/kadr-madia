// Package httpx holds small HTTP helpers shared by the admin and public APIs:
// JSON responses, RFC 9457 problem details and request decoding.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// MaxBodyBytes caps JSON request bodies.
const MaxBodyBytes = 1 << 20

// Problem is an application/problem+json body.
type Problem struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Code   string            `json:"code"`
	Detail string            `json:"detail,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteProblem writes a problem+json response.
func WriteProblem(w http.ResponseWriter, status int, code, detail string) {
	WriteProblemErrors(w, status, code, detail, nil)
}

// WriteProblemErrors writes a problem+json response with field errors.
func WriteProblemErrors(w http.ResponseWriter, status int, code, detail string, fieldErrors map[string]string) {
	p := Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
	if len(fieldErrors) > 0 {
		p.Errors = fieldErrors
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

// ErrBadJSON wraps JSON decoding failures.
var ErrBadJSON = errors.New("invalid JSON body")

// DecodeJSON decodes a size-limited JSON body into dst. Unknown fields are
// ignored for forward compatibility with the UI.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: empty body", ErrBadJSON)
		}
		return fmt.Errorf("%w: %v", ErrBadJSON, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data", ErrBadJSON)
	}
	return nil
}

// ClientIP returns the client address. The Cf-Connecting-Ip header is
// trusted only when the TCP peer is loopback (cloudflared runs locally).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil && addr.IsLoopback() {
		if cf := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); cf != "" {
			if a, err := netip.ParseAddr(cf); err == nil {
				return a.String()
			}
		}
	}
	return host
}
