package pipeline_test

import (
	"strings"
	"testing"
)

func TestChangeVisibilityCopiesBetweenBuckets(t *testing.T) {
	e := newEnv(t)
	a := e.upload(photo("vis"), "org_vis", "public")
	id := a["id"].(string)
	var oldKeys []string
	for _, x := range a["renditions"].([]any) {
		u := x.(map[string]any)["url"].(string)
		oldKeys = append(oldKeys, strings.TrimPrefix(u, cdnBase+"/"))
	}
	if len(oldKeys) == 0 {
		t.Fatalf("no public renditions: %v", a)
	}

	r := e.call("PATCH", "/v1/assets/"+id, map[string]any{"visibility": "private", "metadata": map[string]any{"k": "v"}}, nil)
	expect(t, r, 200, "")
	p := r.obj(t)
	if p["visibility"] != "private" || p["metadata"].(map[string]any)["k"] != "v" {
		t.Fatalf("patched: %s", r.body)
	}
	for _, x := range p["renditions"].([]any) {
		rv := x.(map[string]any)
		if rv["url"] != nil || rv["key"] == nil {
			t.Fatalf("private rendition must expose key only: %v", rv)
		}
		if !e.objectExists(privateBucket, rv["key"].(string)) {
			t.Fatalf("copy missing in the private bucket: %v", rv["key"])
		}
	}
	// The old public objects are removed by GC once the old blob is unreferenced.
	if _, err := e.svc.CollectGarbage(e.ctx); err != nil {
		t.Fatal(err)
	}
	for _, k := range oldKeys {
		if e.objectExists(publicBucket, k) {
			t.Fatalf("public object %s still exists after GC", k)
		}
	}
	// And back to public: Cache-Control of the copies is public.
	r = e.call("PATCH", "/v1/assets/"+id, map[string]any{"visibility": "public"}, nil)
	expect(t, r, 200, "")
	for _, x := range r.obj(t)["renditions"].([]any) {
		if x.(map[string]any)["url"] == nil {
			t.Fatalf("public rendition without url: %v", x)
		}
	}
	if len(e.events("asset.updated")) != 2 {
		t.Fatalf("events: %v", e.events("asset.updated"))
	}
	expect(t, e.call("PATCH", "/v1/assets/"+id, map[string]any{"visibility": "secret"}, nil), 422, "validation_failed")
}
