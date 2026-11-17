package pipeline_test

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/engine/video"
)

// videoEngine returns the real ffmpeg engine, or nil (tests skip) without ffmpeg.
func videoEngine() video.Engine {
	e, err := video.New(video.Config{})
	if err != nil {
		return nil
	}
	return e
}

func TestVideoUploadEndToEnd(t *testing.T) {
	if videoEngine() == nil {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t)
	src := filepath.Join(t.TempDir(), "in.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"testsrc2=s=1920x1080:r=25:d=2", "-f", "lavfi", "-i", "sine=d=2", "-c:v", "libx264", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(src)
	body := ticketBody(data, "org_vid", "public", false)
	body["preset"], body["content_type"], body["filename"] = "video-480p-eco", "video/mp4", "clip.mp4"
	r := e.call("POST", "/v1/uploads", body, nil)
	expect(t, r, 201, "")
	ticket := r.obj(t)
	e.putTicket(ticket, data)
	r = e.call("POST", "/v1/uploads/"+ticket["upload_id"].(string)+"/complete?wait=30s", nil, nil)
	expect(t, r, 200, "")
	a := r.obj(t)
	if a["status"] != "ready" || a["kind"] != "video" {
		t.Fatalf("asset: %s", r.body)
	}
	if d, _ := a["duration_sec"].(float64); d < 1.5 || d > 2.5 {
		t.Fatalf("duration %v", a["duration_sec"])
	}
	var mp4, poster int
	for _, x := range a["renditions"].([]any) {
		rv := x.(map[string]any)
		switch {
		case rv["format"] == "mp4" && rv["name"] == "main":
			mp4++
			if rv["width"].(float64) != 854 {
				t.Fatalf("width %v", rv["width"])
			}
		case rv["name"] == "poster":
			poster++
		}
	}
	if mp4 != 1 || poster != 2 {
		t.Fatalf("renditions: %s", r.body)
	}
	if a["placeholder"] == nil {
		t.Fatal("no placeholder")
	}
}

func TestVideoMultipartUpload(t *testing.T) {
	if videoEngine() == nil {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t)
	src := filepath.Join(t.TempDir(), "big.mp4")
	// Noise at a high bitrate gives a file larger than the 8 MB test threshold.
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"nullsrc=s=1280x720:r=25:d=3,geq=random(1)*255:128:128", "-c:v", "libx264", "-b:v", "30M", "-minrate", "30M", "-maxrate", "30M",
		"-bufsize", "30M", "-pix_fmt", "yuv420p", "-movflags", "+faststart", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(src)
	if len(data) < 9<<20 {
		t.Fatalf("test file too small: %d", len(data))
	}
	body := ticketBody(data, "org_mp", "public", false)
	body["preset"], body["content_type"], body["filename"] = "video-480p-eco", "video/mp4", "big.mp4"
	r := e.call("POST", "/v1/uploads", body, nil)
	expect(t, r, 201, "")
	tk := r.obj(t)
	if tk["method"] != "MULTIPART" || tk["url"] != nil {
		t.Fatalf("ticket: %s", r.body)
	}
	partSize := int(tk["part_size"].(float64))
	var parts []map[string]any
	for _, x := range tk["parts"].([]any) {
		pt := x.(map[string]any)
		n := int(pt["part_number"].(float64))
		lo, hi := (n-1)*partSize, min(n*partSize, len(data))
		req, _ := http.NewRequest("PUT", pt["url"].(string), bytes.NewReader(data[lo:hi]))
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("part %d: %v %v", n, err, resp)
		}
		resp.Body.Close()
		parts = append(parts, map[string]any{"part_number": n, "etag": resp.Header.Get("ETag")})
	}
	if len(parts) < 2 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	id := tk["upload_id"].(string)
	// Completing without parts is rejected while the object does not exist yet.
	expect(t, e.call("POST", "/v1/uploads/"+id+"/complete", nil, nil), 422, "")
	r = e.call("POST", "/v1/uploads/"+id+"/complete?wait=30s", map[string]any{"parts": parts}, nil)
	expect(t, r, 200, "")
	if a := r.obj(t); a["status"] != "ready" || a["kind"] != "video" {
		t.Fatalf("asset: %s", r.body)
	}
	// A retried complete is idempotent.
	expect(t, e.call("POST", "/v1/uploads/"+id+"/complete?wait=5s", map[string]any{"parts": parts}, nil), 200, "")
}
