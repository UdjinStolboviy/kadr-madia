package pipeline_test

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

func TestMultipartResumeAndRefresh(t *testing.T) {
	e := newEnv(t)

	// 1) Explicit multipart:true below threshold (6 MB > 5 MB minimum part size, < 8 MB test threshold).
	small6MB := bytes.Repeat([]byte("line\n"), (6<<20)/5)
	r := e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_mp_res", "visibility": "private",
		"filename": "notes.txt", "content_type": "text/plain", "size": len(small6MB), "multipart": true,
	}, nil)
	expect(t, r, 201, "")
	expTk := r.obj(t)
	if expTk["method"] != "MULTIPART" || int(expTk["part_size"].(float64)) != 5<<20 || len(expTk["parts"].([]any)) != 2 {
		t.Fatalf("explicit multipart ticket: %s", r.body)
	}
	if expTk["session_expires_at"] == nil || expTk["expires_at"] == nil {
		t.Fatalf("missing expiry fields: %s", r.body)
	}
	// Cancel it so it doesn't hold quota or linger.
	expect(t, e.call("DELETE", "/v1/uploads/"+expTk["upload_id"].(string), nil, nil), 204, "")
	// Refreshing an aborted upload fails with 409 upload_aborted.
	expect(t, e.call("POST", "/v1/uploads/"+expTk["upload_id"].(string)+"/parts", nil, nil), 409, "upload_aborted")

	// Files <= MinMultipartPartBytes still use PUT even when multipart:true is requested.
	tiny := []byte("hello world")
	r = e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_mp_res", "visibility": "private",
		"filename": "tiny.txt", "content_type": "text/plain", "size": len(tiny), "multipart": true,
	}, nil)
	expect(t, r, 201, "")
	tinyTk := r.obj(t)
	if tinyTk["method"] != "PUT" {
		t.Fatalf("expected PUT for <= 5MB file, got %v", tinyTk["method"])
	}
	// Refreshing a PUT upload fails with 409 upload_not_multipart.
	expect(t, e.call("POST", "/v1/uploads/"+tinyTk["upload_id"].(string)+"/parts", nil, nil), 409, "upload_not_multipart")
	// Status for PUT upload works too.
	stPut := e.call("GET", "/v1/uploads/"+tinyTk["upload_id"].(string), nil, nil)
	expect(t, stPut, 200, "")
	if stPut.obj(t)["method"] != "PUT" || stPut.obj(t)["status"] != "pending" {
		t.Fatalf("PUT status: %s", stPut.body)
	}
	expect(t, e.call("DELETE", "/v1/uploads/"+tinyTk["upload_id"].(string), nil, nil), 204, "")

	// 2) Full multipart resume flow across expired part URLs (11 MB > 8 MB threshold -> 3 parts of 5, 5, 1 MB).
	data := bytes.Repeat([]byte("abcd\n"), (11<<20)/5)
	r = e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_mp_res", "visibility": "private",
		"filename": "big.txt", "content_type": "text/plain", "size": len(data),
	}, nil)
	expect(t, r, 201, "")
	tk := r.obj(t)
	if tk["method"] != "MULTIPART" {
		t.Fatalf("expected MULTIPART: %s", r.body)
	}
	id := tk["upload_id"].(string)
	partSize := int(tk["part_size"].(float64))
	rawParts := tk["parts"].([]any)
	if len(rawParts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(rawParts))
	}

	// Upload ONLY part 1.
	putPart := func(partObj map[string]any) map[string]any {
		t.Helper()
		n := int(partObj["part_number"].(float64))
		lo, hi := (n-1)*partSize, min(n*partSize, len(data))
		req, err := http.NewRequest("PUT", partObj["url"].(string), bytes.NewReader(data[lo:hi]))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("PUT part %d failed: %v %v", n, err, resp)
		}
		resp.Body.Close()
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("part %d returned empty ETag", n)
		}
		return map[string]any{"part_number": n, "etag": etag}
	}
	p1 := putPart(rawParts[0].(map[string]any))

	// Check GET /v1/uploads/{id}: part 1 is reported in uploaded_parts.
	stRes := e.call("GET", "/v1/uploads/"+id, nil, nil)
	expect(t, stRes, 200, "")
	st := stRes.obj(t)
	if st["status"] != "pending" || int(st["part_size"].(float64)) != partSize || int(st["total_parts"].(float64)) != 3 {
		t.Fatalf("unexpected status: %s", stRes.body)
	}
	upParts := st["uploaded_parts"].([]any)
	if len(upParts) != 1 || int(upParts[0].(map[string]any)["part_number"].(float64)) != 1 ||
		int(upParts[0].(map[string]any)["size"].(float64)) != partSize ||
		upParts[0].(map[string]any)["etag"] != p1["etag"] {
		t.Fatalf("unexpected uploaded_parts: %s", stRes.body)
	}

	// Advance clock past the 1-hour part URL TTL (well within 24h session TTL).
	e.clock.Add(pipeline.DefaultMultipartPartTTL + 10*time.Minute)
	// Sweeper must NOT expire or abort the multipart upload during its 24h session window.
	if sw, err := e.svc.SweepExpiredUploads(e.ctx); err != nil || sw.Expired != 0 || sw.Promoted != 0 {
		t.Fatalf("sweeper touched active multipart session: %+v %v", sw, err)
	}

	// Invalid part_numbers returns 422 validation_failed.
	expect(t, e.call("POST", "/v1/uploads/"+id+"/parts", map[string]any{"part_numbers": []int{99}}, nil), 422, "validation_failed")

	// Explicit part_numbers refresh signs just the requested part.
	refExplicit := e.call("POST", "/v1/uploads/"+id+"/parts", map[string]any{"part_numbers": []int{3}}, nil)
	expect(t, refExplicit, 200, "")
	if len(refExplicit.obj(t)["parts"].([]any)) != 1 || int(refExplicit.obj(t)["parts"].([]any)[0].(map[string]any)["part_number"].(float64)) != 3 {
		t.Fatalf("explicit refresh: %s", refExplicit.body)
	}

	// Empty body refresh auto-signs only missing parts (parts 2 and 3) and echoes uploaded part 1.
	refAuto := e.call("POST", "/v1/uploads/"+id+"/parts", nil, nil)
	expect(t, refAuto, 200, "")
	refObj := refAuto.obj(t)
	if len(refObj["uploaded_parts"].([]any)) != 1 {
		t.Fatalf("refresh uploaded_parts: %s", refAuto.body)
	}
	missingParts := refObj["parts"].([]any)
	if len(missingParts) != 2 ||
		int(missingParts[0].(map[string]any)["part_number"].(float64)) != 2 ||
		int(missingParts[1].(map[string]any)["part_number"].(float64)) != 3 {
		t.Fatalf("expected parts 2 & 3, got: %s", refAuto.body)
	}

	// Upload missing parts using the refreshed URLs and complete using combined ETags.
	p2 := putPart(missingParts[0].(map[string]any))
	p3 := putPart(missingParts[1].(map[string]any))
	compRes := e.call("POST", "/v1/uploads/"+id+"/complete?wait=20s", map[string]any{
		"parts": []map[string]any{p1, p2, p3},
	}, nil)
	expect(t, compRes, 200, "")
	asset := compRes.obj(t)
	if asset["status"] != "ready" {
		t.Fatalf("completed asset not ready: %s", compRes.body)
	}

	// GET /v1/uploads/{id} now reports uploaded/done and asset_id.
	finalSt := e.call("GET", "/v1/uploads/"+id, nil, nil)
	expect(t, finalSt, 200, "")
	if finalSt.obj(t)["asset_id"] != asset["id"] {
		t.Fatalf("expected asset_id %v, got %s", asset["id"], finalSt.body)
	}
	// Refreshing after completion returns 409 upload_not_pending.
	expect(t, e.call("POST", "/v1/uploads/"+id+"/parts", nil, nil), 409, "upload_not_pending")

	// 3) Session expiration & sweeper abort after 24h + grace.
	r = e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_mp_res", "visibility": "private",
		"filename": "stale.txt", "content_type": "text/plain", "size": len(data),
	}, nil)
	expect(t, r, 201, "")
	staleID := r.obj(t)["upload_id"].(string)

	// Past session_expires_at (before grace): refresh rejects with 410 upload_expired.
	e.clock.Add(pipeline.DefaultMultipartSessionTTL + time.Second)
	expect(t, e.call("POST", "/v1/uploads/"+staleID+"/parts", nil, nil), 410, "upload_expired")

	// Past grace: sweeper expires & aborts multipart upload.
	e.clock.Add(pipeline.DefaultSweepGrace + time.Second)
	sw, err := e.svc.SweepExpiredUploads(e.ctx)
	if err != nil || sw.Expired != 1 {
		t.Fatalf("sweeper should expire stale multipart upload: %+v %v", sw, err)
	}
	expect(t, e.call("POST", "/v1/uploads/"+staleID+"/complete", nil, nil), 410, "upload_expired")
}
