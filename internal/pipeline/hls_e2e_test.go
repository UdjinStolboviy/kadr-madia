package pipeline_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/preset"
)

func TestVideoHLSLadder(t *testing.T) {
	if videoEngine() == nil {
		t.Skip("ffmpeg not installed")
	}
	e := newEnv(t)
	// Preset with hls=true (fast encode for the test).
	params := preset.VideoParams{Kind: "video", Video: preset.VideoCodec{Codec: "h264", MaxWidth: 1280, FPS: 25, CRF: 30, Preset: "ultrafast", HW: "off"},
		Audio: preset.AudioCodec{Codec: "aac", Bitrate: "48k", Channels: 1, SampleRate: 44100}, Container: preset.Container{Format: "mp4", Faststart: true},
		Poster: preset.Poster{MaxBytes: 15360}, Limits: preset.VideoLimits{MaxInputBytes: 1 << 30, MaxDurationSec: 300, MaxWidth: 4096, MaxHeight: 4096}, HLS: true}
	pj, _ := json.Marshal(params)
	must(t, e.store.CreatePreset(e.ctx, &db.Preset{ID: db.NewID(db.PrefixPreset), ProjectID: e.project.ID, Slug: "video-hls", Kind: "video",
		Description: "hls", CurrentVersion: 1, Params: pj, CreatedAt: time.Now().UTC()}, "test"))

	src := filepath.Join(t.TempDir(), "in.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=25:d=6",
		"-f", "lavfi", "-i", "sine=d=6", "-c:v", "libx264", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	data, _ := os.ReadFile(src)
	body := ticketBody(data, "org_hls", "public", false)
	body["preset"], body["content_type"], body["filename"] = "video-hls", "video/mp4", "clip.mp4"
	r := e.call("POST", "/v1/uploads", body, nil)
	expect(t, r, 201, "")
	ticket := r.obj(t)
	e.putTicket(ticket, data)
	r = e.call("POST", "/v1/uploads/"+ticket["upload_id"].(string)+"/complete?wait=60s", nil, nil)
	expect(t, r, 200, "")
	a := r.obj(t)
	if a["status"] != "ready" {
		t.Fatalf("asset: %s", r.body)
	}
	var master map[string]any
	var total float64
	for _, x := range a["renditions"].([]any) {
		rv := x.(map[string]any)
		total += rv["bytes"].(float64)
		if rv["format"] == "m3u8" {
			master = rv
		}
	}
	if master == nil || master["name"] != "hls" || !strings.HasSuffix(master["url"].(string), "/hls/master.m3u8") || master["bytes"].(float64) < 1000 {
		t.Fatalf("hls rendition: %s", r.body)
	}
	masterKey := strings.TrimPrefix(master["url"].(string), cdnBase+"/")
	prefix := strings.TrimSuffix(masterKey, "master.m3u8")

	objs, err := e.driver.List(e.ctx, publicBucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	var sizeSum int64
	var segs, inits, lists int
	for _, o := range objs {
		sizeSum += o.Size
		var ct string
		switch {
		case strings.HasSuffix(o.Key, ".m3u8"):
			lists++
			ct = "application/vnd.apple.mpegurl"
		case strings.HasSuffix(o.Key, ".m4s"):
			segs++
			ct = "video/iso.segment"
		case strings.HasSuffix(o.Key, ".mp4"):
			inits++
			ct = "video/mp4"
		}
		if po, ok := e.driver.opts(publicBucket, o.Key); !ok || po.ContentType != ct || po.CacheControl == "" {
			t.Fatalf("put options of %s: %+v %v", o.Key, po, ok)
		}
	}
	if lists != 4 || inits != 3 || segs < 6 { // master + 3 variants (360/480/720)
		t.Fatalf("package: %d playlists, %d inits, %d segments", lists, inits, segs)
	}
	if sizeSum != int64(master["bytes"].(float64)) {
		t.Fatalf("master bytes %v != package size %d", master["bytes"], sizeSum)
	}

	// Visibility change copies the whole package; GC removes the old one.
	id := a["id"].(string)
	asset, _ := e.store.GetAsset(e.ctx, id)
	oldBlob := *asset.BlobID
	r = e.call("PATCH", "/v1/assets/"+id, map[string]any{"visibility": "private"}, nil)
	expect(t, r, 200, "")
	var newMaster string
	for _, x := range r.obj(t)["renditions"].([]any) {
		if rv := x.(map[string]any); rv["format"] == "m3u8" {
			newMaster = rv["key"].(string)
		}
	}
	newPrefix := strings.TrimSuffix(newMaster, "master.m3u8")
	if newMaster == "" || newPrefix == prefix {
		t.Fatalf("private master key: %q", newMaster)
	}
	moved, _ := e.driver.List(e.ctx, privateBucket, newPrefix)
	if len(moved) != len(objs) {
		t.Fatalf("copied %d objects, want %d", len(moved), len(objs))
	}
	if _, err := e.svc.CollectGarbage(e.ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := e.driver.List(e.ctx, publicBucket, prefix); len(left) != 0 {
		t.Fatalf("%d public hls objects survived GC of blob %s", len(left), oldBlob)
	}

	// Deleting the asset removes the private package.
	expect(t, e.call("DELETE", "/v1/assets/"+id, nil, nil), 204, "")
	waitUntil(t, "hls GC", func() bool {
		left, _ := e.driver.List(e.ctx, privateBucket, newPrefix)
		return len(left) == 0
	})
}
