package pipeline_test

import (
	"testing"
)

func TestCompressionModesInUploads(t *testing.T) {
	e := newEnv(t)
	up := func(extra map[string]any) response {
		b := ticketBody(photo("m"+t.Name()), "org_m", "public", false)
		delete(b, "preset")
		for k, v := range extra {
			b[k] = v
		}
		return e.call("POST", "/v1/uploads", b, nil)
	}
	complete := func(r response) map[string]any {
		expect(t, r, 201, "")
		tk := r.obj(t)
		e.putTicket(tk, photo("m"+t.Name()))
		c := e.call("POST", "/v1/uploads/"+tk["upload_id"].(string)+"/complete?wait=10s", nil, nil)
		expect(t, c, 200, "")
		return c.obj(t)
	}

	if a := complete(up(map[string]any{"mode": "compact"})); a["preset"] != "image-compact" {
		t.Fatalf("compact → %v", a["preset"])
	}
	// No preset and no mode: the project default (balanced) applies.
	e.setDefaultMode("quality")
	data := photo("other")
	b := ticketBody(data, "org_m", "public", false)
	delete(b, "preset")
	r := e.call("POST", "/v1/uploads", b, nil)
	expect(t, r, 201, "")
	tk := r.obj(t)
	e.putTicket(tk, data)
	c := e.call("POST", "/v1/uploads/"+tk["upload_id"].(string)+"/complete?wait=10s", nil, nil)
	expect(t, c, 200, "")
	if c.obj(t)["preset"] != "image-quality" {
		t.Fatalf("default mode → %v", c.obj(t)["preset"])
	}

	both := up(map[string]any{"mode": "compact", "preset": "photo-lite"})
	expect(t, both, 422, "validation_failed")
	if both.obj(t)["errors"].(map[string]any)["mode"] == nil {
		t.Fatalf("%s", both.body)
	}
	bad := up(map[string]any{"mode": "ultra"})
	expect(t, bad, 422, "validation_failed")
}
