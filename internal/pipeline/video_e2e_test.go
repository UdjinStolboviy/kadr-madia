package pipeline_test

import (
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
