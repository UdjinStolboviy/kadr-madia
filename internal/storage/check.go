package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CheckTarget is what a connectivity test runs against.
type CheckTarget struct {
	Driver        Driver
	PublicBucket  string
	PrivateBucket string
	PublicBaseURL string // public objects must be readable at PublicBaseURL/<key>
	// HTTPClient performs the presigned PUT and the public GET.
	HTTPClient *http.Client
}

// CheckResult is one named check.
type CheckResult struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	DurationMS int64  `json:"duration_ms"`
}

// TestResult is the outcome of RunChecks.
type TestResult struct {
	OK     bool          `json:"ok"`
	Checks []CheckResult `json:"checks"`
}

// Check names, in execution order (docs/ADMIN_API.md).
const (
	CheckPrivatePut        = "private_put"
	CheckPrivateHead       = "private_head"
	CheckPrivateDelete     = "private_delete"
	CheckPrivatePresignPut = "private_presign_put"
	CheckPublicPut         = "public_put"
	CheckPublicGetBaseURL  = "public_get_via_base_url"
	CheckPublicDelete      = "public_delete"
)

const perCheckTimeout = 10 * time.Second

// RunChecks writes, reads and deletes small probe objects in both buckets.
// Probe keys live under tmp/ (private; covered by the tmp/ lifecycle rule)
// and _kadr-healthcheck/ (public). Checks that depend on a failed check are
// reported as failed with a "skipped" detail.
func RunChecks(ctx context.Context, t CheckTarget) TestResult {
	hc := t.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: perCheckTimeout}
	}
	id := randHex(8)
	payload := []byte("kadr storage check " + id + "\n")
	privKey := "tmp/_kadr-healthcheck/" + id + ".txt"
	presignKey := "tmp/_kadr-healthcheck/" + id + "-presigned.txt"
	pubKey := "_kadr-healthcheck/" + id + ".txt"

	var res TestResult
	run := func(name string, deps []string, fn func(ctx context.Context) (string, error)) bool {
		for _, d := range deps {
			if !passed(res.Checks, d) {
				res.Checks = append(res.Checks, CheckResult{Name: name, Detail: "skipped: " + d + " failed"})
				return false
			}
		}
		cctx, cancel := context.WithTimeout(ctx, perCheckTimeout)
		defer cancel()
		start := time.Now()
		detail, err := fn(cctx)
		cr := CheckResult{Name: name, OK: err == nil, Detail: detail, DurationMS: time.Since(start).Milliseconds()}
		if err != nil {
			cr.Detail = err.Error()
		}
		res.Checks = append(res.Checks, cr)
		return err == nil
	}

	run(CheckPrivatePut, nil, func(ctx context.Context) (string, error) {
		err := t.Driver.Put(ctx, t.PrivateBucket, privKey, bytes.NewReader(payload), int64(len(payload)), PutOptions{ContentType: "text/plain"})
		return fmt.Sprintf("wrote %s/%s (%d B)", t.PrivateBucket, privKey, len(payload)), err
	})
	run(CheckPrivateHead, []string{CheckPrivatePut}, func(ctx context.Context) (string, error) {
		info, err := t.Driver.Head(ctx, t.PrivateBucket, privKey)
		if err != nil {
			return "", err
		}
		if info.Size != int64(len(payload)) {
			return "", fmt.Errorf("size mismatch: got %d B, want %d B", info.Size, len(payload))
		}
		return fmt.Sprintf("size %d B, etag %s", info.Size, info.ETag), nil
	})
	run(CheckPrivateDelete, []string{CheckPrivatePut}, func(ctx context.Context) (string, error) {
		return "deleted", t.Driver.Delete(ctx, t.PrivateBucket, privKey)
	})
	run(CheckPrivatePresignPut, nil, func(ctx context.Context) (string, error) {
		pr, err := t.Driver.PresignPut(ctx, t.PrivateBucket, presignKey, PresignPutOptions{
			ContentType: "text/plain", ContentLength: int64(len(payload)), TTL: 5 * time.Minute,
		})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, pr.Method, pr.URL, bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		for k, v := range pr.Headers {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "text/plain")
		req.ContentLength = int64(len(payload))
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("presigned PUT: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("presigned PUT returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		_ = t.Driver.Delete(ctx, t.PrivateBucket, presignKey) // best-effort cleanup
		return fmt.Sprintf("presigned PUT accepted (%d)", resp.StatusCode), nil
	})
	run(CheckPublicPut, nil, func(ctx context.Context) (string, error) {
		err := t.Driver.Put(ctx, t.PublicBucket, pubKey, bytes.NewReader(payload), int64(len(payload)),
			PutOptions{ContentType: "text/plain", CacheControl: "no-store"})
		return fmt.Sprintf("wrote %s/%s (%d B)", t.PublicBucket, pubKey, len(payload)), err
	})
	run(CheckPublicGetBaseURL, []string{CheckPublicPut}, func(ctx context.Context) (string, error) {
		if t.PublicBaseURL == "" {
			return "", errors.New("public_base_url is empty")
		}
		u := strings.TrimRight(t.PublicBaseURL, "/") + "/" + pubKey
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return "", fmt.Errorf("GET %s: %w", u, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s returned %d", u, resp.StatusCode)
		}
		if !bytes.Equal(body, payload) {
			return "", fmt.Errorf("GET %s returned unexpected content", u)
		}
		return fmt.Sprintf("GET %s → 200", u), nil
	})
	run(CheckPublicDelete, []string{CheckPublicPut}, func(ctx context.Context) (string, error) {
		return "deleted", t.Driver.Delete(ctx, t.PublicBucket, pubKey)
	})

	res.OK = true
	for _, c := range res.Checks {
		res.OK = res.OK && c.OK
	}
	return res
}

func passed(checks []CheckResult, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return c.OK
		}
	}
	return false
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
