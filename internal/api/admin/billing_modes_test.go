package admin

import (
	"strings"
	"testing"
)

func TestBillingAndModes(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	c.login(t, nil)
	r := c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme"}, true)
	if r.code != 201 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	pid := r.json(t)["id"].(string)
	if r.json(t)["default_mode"] != "balanced" {
		t.Fatalf("default mode: %s", r.body)
	}

	// Rates, validation and an empty month.
	expectProblem(t, c.do("PUT", "/admin/api/billing/rates", map[string]any{"currency": "USDX", "per_gb_ingested": -1}, true), 422, "validation_failed")
	r = c.do("PUT", "/admin/api/billing/rates", map[string]any{"currency": "EUR", "per_gb_stored_month": 0.02, "per_gb_ingested": 0.01,
		"per_video_minute": 0.05, "per_1000_uploads": 0.5, "minimum_monthly_fee": 5}, true)
	if r.code != 200 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	r = c.do("GET", "/admin/api/projects/"+pid+"/billing", nil, false)
	rep := r.json(t)
	if r.code != 200 || rep["currency"] != "EUR" || rep["total"].(float64) != 5 || len(rep["lines"].([]any)) != 4 {
		t.Fatalf("billing: %s", r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/projects/"+pid+"/billing?month=2026-13", nil, false), 422, "validation_failed")
	if r := c.do("GET", "/admin/api/projects/"+pid+"/billing?format=csv", nil, false); r.code != 200 || !strings.HasPrefix(string(r.body), "day,uploads") {
		t.Fatalf("csv: %d %s", r.code, r.body)
	}

	// Compression modes.
	r = c.do("GET", "/admin/api/projects/"+pid+"/modes", nil, false)
	modes := r.json(t)["modes"].([]any)
	if r.code != 200 || len(modes) != 3 || modes[0].(map[string]any)["mode"] != "compact" {
		t.Fatalf("modes: %s", r.body)
	}
	for _, m := range modes {
		mm := m.(map[string]any)
		if mm["image"].(map[string]any)["slug"] == nil || mm["video"].(map[string]any)["slug"] == nil {
			t.Fatalf("mode without presets: %v", mm)
		}
	}
	expectProblem(t, c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"default_mode": "ultra"}, true), 422, "validation_failed")
	if r := c.do("PATCH", "/admin/api/projects/"+pid, map[string]any{"default_mode": "quality"}, true); r.code != 200 || r.json(t)["default_mode"] != "quality" {
		t.Fatalf("patch: %s", r.body)
	}

	// Edit a video mode preset, see it flagged as customized, then reset.
	r = c.do("GET", "/admin/api/projects/"+pid+"/modes", nil, false)
	video := modes[0].(map[string]any)["video"].(map[string]any)
	params := video["params"].(map[string]any)
	params["video"].(map[string]any)["crf"] = 33
	r = c.do("POST", "/admin/api/projects/"+pid+"/presets/video-compact/versions", map[string]any{"params": params}, true)
	if r.code != 200 || r.json(t)["current_version"].(float64) != 2 {
		t.Fatalf("version: %d %s", r.code, r.body)
	}
	params["video"].(map[string]any)["crf"] = 99
	expectProblem(t, c.do("POST", "/admin/api/projects/"+pid+"/presets/video-compact/versions", map[string]any{"params": params}, true), 422, "validation_failed")
	r = c.do("GET", "/admin/api/projects/"+pid+"/modes", nil, false)
	if r.json(t)["modes"].([]any)[0].(map[string]any)["customized"].(map[string]any)["video"] != true {
		t.Fatalf("not flagged: %s", r.body)
	}
	if r = c.do("POST", "/admin/api/projects/"+pid+"/presets/video-compact/reset", nil, true); r.code != 200 {
		t.Fatalf("reset: %d %s", r.code, r.body)
	}
	r = c.do("GET", "/admin/api/projects/"+pid+"/modes", nil, false)
	if r.json(t)["modes"].([]any)[0].(map[string]any)["customized"].(map[string]any)["video"] != false {
		t.Fatalf("still flagged after reset: %s", r.body)
	}
	expectProblem(t, c.do("POST", "/admin/api/projects/"+pid+"/presets/nope-nope/reset", nil, true), 422, "validation_failed")
}
