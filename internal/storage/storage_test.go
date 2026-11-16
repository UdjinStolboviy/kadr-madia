package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"

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
	if _, err := d.CreateMultipart(ctx, "priv", "x", ""); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("multipart: %v", err)
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
