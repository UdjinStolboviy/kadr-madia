package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// FakeS3 starts an in-process S3 server with the given buckets.
func newFake(t *testing.T, buckets ...string) (*httptest.Server, *S3) {
	t.Helper()
	backend := s3mem.New()
	for _, b := range buckets {
		if err := backend.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(ts.Close)
	d, err := NewS3(Config{Provider: "r2", Endpoint: ts.URL, Region: "auto", AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	return ts, d
}

func TestS3DriverBasics(t *testing.T) {
	ctx := context.Background()
	_, d := newFake(t, "priv", "pub")
	body := []byte("hello")
	if err := d.Put(ctx, "priv", "a/b.txt", bytes.NewReader(body), int64(len(body)), PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	info, err := d.Head(ctx, "priv", "a/b.txt")
	if err != nil || info.Size != 5 {
		t.Fatalf("head: %+v %v", info, err)
	}
	rc, _, err := d.Get(ctx, "priv", "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, body) {
		t.Fatalf("get = %q", got)
	}
	if err := d.Copy(ctx, "priv", "a/b.txt", "pub", "c.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Head(ctx, "pub", "c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, "priv", "a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Head(ctx, "priv", "a/b.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("head after delete: %v", err)
	}
	pg, err := d.PresignGet(ctx, "pub", "c.txt", 0)
	if err != nil || pg.URL == "" {
		t.Fatalf("presign get: %v", err)
	}
}

func TestMultipartRoundTrip(t *testing.T) {
	_, d := newFake(t, "priv")
	ctx := context.Background()
	u, err := d.CreateMultipart(ctx, "priv", "big.bin", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if initial, err := d.ListParts(ctx, u); err != nil || len(initial) != 0 {
		t.Fatalf("initial ListParts = %+v, err=%v", initial, err)
	}
	sizes := []int{5 << 20, 5 << 20, 1 << 20}
	var parts []CompletedPart
	total := 0
	for i, n := range sizes {
		pr, err := d.PresignUploadPart(ctx, u, int32(i+1), 0)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(pr.Method, pr.URL, bytes.NewReader(make([]byte, n)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("part %d: %v %v", i+1, err, resp)
		}
		resp.Body.Close()
		parts = append(parts, CompletedPart{Number: int32(i + 1), ETag: resp.Header.Get("ETag")})
		total += n
		if i == 1 {
			listed, err := d.ListParts(ctx, u)
			if err != nil || len(listed) != 2 {
				t.Fatalf("ListParts mid-upload = %+v, err=%v", listed, err)
			}
			for j := range listed {
				if listed[j].Number != int32(j+1) || listed[j].Size != int64(sizes[j]) || listed[j].ETag == "" {
					t.Fatalf("unexpected listed part %d: %+v", j, listed[j])
				}
			}
		}
	}
	if err := d.CompleteMultipart(ctx, u, parts); err != nil {
		t.Fatal(err)
	}
	info, err := d.Head(ctx, "priv", "big.bin")
	if err != nil || info.Size != int64(total) {
		t.Fatalf("head: %+v %v", info, err)
	}
	u2, _ := d.CreateMultipart(ctx, "priv", "gone.bin", "")
	if err := d.AbortMultipart(ctx, u2); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ListParts(ctx, u2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListParts after abort = %v, want ErrNotFound", err)
	}
}

func TestRunChecksAgainstFake(t *testing.T) {
	ts, d := newFake(t, "kadr-private", "kadr-public")
	res := RunChecks(context.Background(), CheckTarget{
		Driver: d, PublicBucket: "kadr-public", PrivateBucket: "kadr-private",
		PublicBaseURL: ts.URL + "/kadr-public", // path-style: the fake serves objects anonymously
	})
	if !res.OK {
		t.Fatalf("checks failed: %+v", res.Checks)
	}
	want := []string{CheckPrivatePut, CheckPrivateHead, CheckPrivateDelete, CheckPrivatePresignPut, CheckPublicPut, CheckPublicGetBaseURL, CheckPublicDelete}
	if len(res.Checks) != len(want) {
		t.Fatalf("got %d checks", len(res.Checks))
	}
	for i, c := range res.Checks {
		if c.Name != want[i] || !c.OK || c.Detail == "" {
			t.Fatalf("check %d = %+v", i, c)
		}
	}
}

func TestRunChecksReportsFailures(t *testing.T) {
	ts, d := newFake(t, "kadr-public") // private bucket missing
	res := RunChecks(context.Background(), CheckTarget{
		Driver: d, PublicBucket: "kadr-public", PrivateBucket: "kadr-private",
		PublicBaseURL: ts.URL + "/wrong-bucket",
	})
	if res.OK {
		t.Fatal("expected failure")
	}
	byName := map[string]CheckResult{}
	for _, c := range res.Checks {
		byName[c.Name] = c
	}
	if byName[CheckPrivatePut].OK || byName[CheckPrivateHead].Detail != "skipped: private_put failed" {
		t.Fatalf("private: %+v %+v", byName[CheckPrivatePut], byName[CheckPrivateHead])
	}
	if !byName[CheckPublicPut].OK || byName[CheckPublicGetBaseURL].OK || !byName[CheckPublicDelete].OK {
		t.Fatalf("public: %+v", res.Checks)
	}
}

func TestS3List(t *testing.T) {
	ctx := context.Background()
	_, d := newFake(t, "pub")
	for _, k := range []string{"p/b1/hls/master.m3u8", "p/b1/hls/v0_00001.m4s", "p/b1/main.mp4", "p/b10/hls/x.m4s"} {
		if err := d.Put(ctx, "pub", k, bytes.NewReader([]byte("x")), 1, PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.List(ctx, "pub", "p/b1/hls/")
	if err != nil || len(got) != 2 {
		t.Fatalf("list: %v %v", got, err)
	}
	if got, err = d.List(ctx, "pub", "nothing/"); err != nil || len(got) != 0 {
		t.Fatalf("empty list: %v %v", got, err)
	}
}

func TestGetRange(t *testing.T) {
	ctx := context.Background()
	_, d := newFake(t, "priv")
	body := []byte("0123456789")
	if err := d.Put(ctx, "priv", "r.bin", bytes.NewReader(body), int64(len(body)),
		PutOptions{ContentType: "application/pdf", ContentDisposition: `attachment; filename="a.pdf"`}); err != nil {
		t.Fatal(err)
	}
	rc, info, err := d.GetRange(ctx, "priv", "r.bin", 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "3456" || info.Size != 10 {
		t.Fatalf("range: %q size %d", got, info.Size)
	}
	h, err := d.Head(ctx, "priv", "r.bin")
	if err != nil || h.ContentDisposition != `attachment; filename="a.pdf"` {
		t.Fatalf("head disposition: %q %v", h.ContentDisposition, err)
	}
	if _, _, err := d.GetRange(ctx, "priv", "missing", 0, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestPresignDownloadStable(t *testing.T) {
	ctx := context.Background()
	_, d := newFake(t, "priv")
	base := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	opts := func(now time.Time) PresignGetOptions {
		return PresignGetOptions{TTL: time.Hour, StableWindow: time.Hour, Now: now, ContentDisposition: `attachment; filename="a.pdf"`}
	}
	a, err := d.PresignDownload(ctx, "priv", "acme/x/main.pdf", opts(base.Add(5*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := d.PresignDownload(ctx, "priv", "acme/x/main.pdf", opts(base.Add(50*time.Minute)))
	c, _ := d.PresignDownload(ctx, "priv", "acme/x/main.pdf", opts(base.Add(65*time.Minute)))
	if a.URL != b.URL || a.URL == c.URL {
		t.Fatalf("stability: same window equal=%v, next window differs=%v", a.URL == b.URL, a.URL != c.URL)
	}
	u, _ := url.Parse(a.URL)
	q := u.Query()
	if q.Get("X-Amz-Date") != "20261119T100000Z" || q.Get("X-Amz-Expires") != "7200" ||
		q.Get("response-content-disposition") != `attachment; filename="a.pdf"` || !strings.HasSuffix(u.Path, "/priv/acme/x/main.pdf") {
		t.Fatalf("url %s", a.URL)
	}
	// Remaining validity is never below the TTL inside a window.
	if b.ExpiresAt.Sub(base.Add(50*time.Minute)) < time.Hour {
		t.Fatalf("expires %v", b.ExpiresAt)
	}
	long, _ := d.PresignDownload(ctx, "priv", "k", PresignGetOptions{TTL: 30 * 24 * time.Hour, Now: base})
	if lq, _ := url.Parse(long.URL); lq.Query().Get("X-Amz-Expires") != "604800" {
		t.Fatalf("cap: %s", long.URL)
	}
}
