package pipeline_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func (e *env) waitStatus(id, want string) map[string]any {
	e.t.Helper()
	for i := 0; i < 200; i++ {
		r := e.call("GET", "/v1/assets/"+id, nil, nil)
		expect(e.t, r, 200, "")
		if a := r.obj(e.t); a["status"] == want {
			return a
		}
		time.Sleep(25 * time.Millisecond)
	}
	e.t.Fatalf("asset %s never became %s", id, want)
	return nil
}

func TestImportURL(t *testing.T) {
	e := newEnv(t)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.jpg" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(photo("remote"))
	}))
	defer src.Close()

	r := e.call("POST", "/v1/imports/url", map[string]any{"url": src.URL + "/a.jpg", "preset": "photo-lite", "namespace": "org_i", "visibility": "public"}, nil)
	expect(t, r, 202, "")
	a := r.obj(t)
	if a["status"] != "processing" {
		t.Fatalf("%s", r.body)
	}
	ready := e.waitStatus(a["id"].(string), "ready")
	if len(ready["renditions"].([]any)) == 0 {
		t.Fatal("no renditions")
	}

	r = e.call("POST", "/v1/imports/url", map[string]any{"url": src.URL + "/missing.jpg", "preset": "photo-lite", "namespace": "org_i", "visibility": "public"}, nil)
	expect(t, r, 202, "")
	failed := e.waitStatus(r.obj(t)["id"].(string), "failed")
	if failed["error"].(map[string]any)["code"] != "import_failed" {
		t.Fatalf("%v", failed["error"])
	}

	expect(t, e.call("POST", "/v1/imports/url", map[string]any{"url": "ftp://x/y.jpg", "preset": "photo-lite", "namespace": "org_i", "visibility": "public"}, nil), 422, "validation_failed")
	expect(t, e.call("POST", "/v1/imports/cloud", map[string]any{"connection_id": "con_nope", "file_id": "1", "preset": "photo-lite", "namespace": "org_i", "visibility": "public"}, nil), 404, "not_found")
}
