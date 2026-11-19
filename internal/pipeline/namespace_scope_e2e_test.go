package pipeline_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/UdjinStolboviy/kadr-madia/internal/auth"
	"github.com/UdjinStolboviy/kadr-madia/internal/db"
	"github.com/UdjinStolboviy/kadr-madia/internal/pipeline"
)

func TestNamespaceScopeE2E(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()

	// Rotate signing key for /v1/signed-urls checks.
	enc, err := e.kr.Encrypt([]byte("s3cret"), pipeline.SigningKeySecretAAD("ns-k1"))
	must(t, err)
	must(t, e.store.RotateSigningKey(e.ctx, &db.SigningKey{ID: "ns-k1", ProjectID: e.project.ID, SecretEnc: enc, CreatedAt: now}))

	// Issue a namespace-scoped API key restricted to org_a.
	gen, err := auth.GenerateAPIKey("live", pepper)
	must(t, err)
	must(t, e.store.CreateAPIKey(e.ctx, &db.APIKey{
		ID:         db.NewID(db.PrefixAPIKey),
		ProjectID:  e.project.ID,
		Name:       "org-a-only",
		Prefix:     gen.Prefix,
		SecretHash: gen.Hash,
		Scopes:     auth.AllScopes,
		Namespaces: []string{"org_a"},
		CreatedAt:  now,
	}))
	unrestricted := e.key
	scoped := gen.Secret
	scopedHdr := map[string]string{"Authorization": "Bearer " + scoped}

	// 1. GET /v1/ping reports namespaces only on restricted keys.
	r := e.call("GET", "/v1/ping", nil, nil)
	expect(t, r, 200, "")
	if _, hasNS := r.obj(t)["namespaces"]; hasNS {
		t.Fatalf("unrestricted /v1/ping should omit namespaces: %s", r.body)
	}
	r = e.call("GET", "/v1/ping", nil, scopedHdr)
	expect(t, r, 200, "")
	nsList, ok := r.obj(t)["namespaces"].([]any)
	if !ok || len(nsList) != 1 || nsList[0] != "org_a" {
		t.Fatalf("scoped /v1/ping namespaces = %v, want [org_a]", r.obj(t)["namespaces"])
	}

	// 2. Seed assets in org_a and org_b using unrestricted key.
	assetA := e.upload(photo("ns-a"), "org_a", "private")
	assetB := e.upload(photo("ns-b"), "org_b", "private")
	idA := assetA["id"].(string)
	idB := assetB["id"].(string)

	// 3. POST /v1/uploads allowed for org_a, forbidden for org_b.
	r = e.call("POST", "/v1/uploads", ticketBody(photo("up-b"), "org_b", "private", false), scopedHdr)
	expect(t, r, 403, pipeline.CodeNamespaceForbidden)

	r = e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_a", "visibility": "private",
		"filename": "a.txt", "content_type": "text/plain", "size": 6 << 20, "multipart": true,
	}, scopedHdr)
	expect(t, r, 201, "")
	upA := r.obj(t)["upload_id"].(string)

	// Seed a multipart upload in org_b using unrestricted key.
	r = e.call("POST", "/v1/uploads", map[string]any{
		"mode": "balanced", "namespace": "org_b", "visibility": "private",
		"filename": "b.txt", "content_type": "text/plain", "size": 6 << 20, "multipart": true,
	}, nil)
	expect(t, r, 201, "")
	upB := r.obj(t)["upload_id"].(string)

	// 4. GET /v1/uploads/{id}, POST /v1/uploads/{id}/parts, complete, abort.
	expect(t, e.call("GET", "/v1/uploads/"+upA, nil, scopedHdr), 200, "")
	expect(t, e.call("GET", "/v1/uploads/"+upB, nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)

	expect(t, e.call("POST", "/v1/uploads/"+upA+"/parts", map[string]any{"part_numbers": []int{1}}, scopedHdr), 200, "")
	expect(t, e.call("POST", "/v1/uploads/"+upB+"/parts", map[string]any{"part_numbers": []int{1}}, scopedHdr), 403, pipeline.CodeNamespaceForbidden)

	expect(t, e.call("POST", "/v1/uploads/"+upB+"/complete", nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("DELETE", "/v1/uploads/"+upB, nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)

	expect(t, e.call("DELETE", "/v1/uploads/"+upA, nil, scopedHdr), 204, "")
	expect(t, e.call("DELETE", "/v1/uploads/"+upB, nil, nil), 204, "")

	// 5. POST /v1/assets server-side upload.
	e.key = scoped
	srvA := e.serverUpload(t, photo("srv-a"), "org_a", "public", 201)
	_ = e.serverUpload(t, photo("srv-b"), "org_b", "public", 403)
	e.key = unrestricted

	// 6. POST /v1/imports/url.
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(photo("imp"))
	}))
	defer src.Close()
	expect(t, e.call("POST", "/v1/imports/url", map[string]any{
		"url": src.URL + "/a.jpg", "preset": "photo-lite", "namespace": "org_a", "visibility": "public",
	}, scopedHdr), 202, "")
	expect(t, e.call("POST", "/v1/imports/url", map[string]any{
		"url": src.URL + "/b.jpg", "preset": "photo-lite", "namespace": "org_b", "visibility": "public",
	}, scopedHdr), 403, pipeline.CodeNamespaceForbidden)

	// 7. GET /v1/assets (filtered & unfiltered).
	expect(t, e.call("GET", "/v1/assets?namespace=org_b", nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	listNS := e.call("GET", "/v1/assets?namespace=org_a", nil, scopedHdr)
	expect(t, listNS, 200, "")
	for _, it := range listNS.obj(t)["items"].([]any) {
		if it.(map[string]any)["namespace"] != "org_a" {
			t.Fatalf("unexpected namespace in filtered list: %v", it)
		}
	}
	listAll := e.call("GET", "/v1/assets", nil, scopedHdr)
	expect(t, listAll, 200, "")
	itemsAll := listAll.obj(t)["items"].([]any)
	if len(itemsAll) < 2 {
		t.Fatalf("expected at least 2 org_a assets, got %d", len(itemsAll))
	}
	for _, it := range itemsAll {
		if it.(map[string]any)["namespace"] != "org_a" {
			t.Fatalf("unfiltered list leaked namespace %v", it.(map[string]any)["namespace"])
		}
	}

	// 8. GET / PATCH / DELETE /v1/assets/{id}.
	expect(t, e.call("GET", "/v1/assets/"+idB, nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("PATCH", "/v1/assets/"+idB, map[string]any{"metadata": map[string]any{"k": "v"}}, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("DELETE", "/v1/assets/"+idB, nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)

	expect(t, e.call("GET", "/v1/assets/"+idA, nil, scopedHdr), 200, "")
	expect(t, e.call("PATCH", "/v1/assets/"+srvA["id"].(string), map[string]any{"metadata": map[string]any{"k": "v"}}, scopedHdr), 200, "")

	// 9. POST /v1/signed-urls.
	expect(t, e.call("POST", "/v1/signed-urls", map[string]any{
		"items": []map[string]any{{"asset_id": idB, "rendition": "main.avif", "ttl": 300}},
	}, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("POST", "/v1/signed-urls", map[string]any{
		"items": []map[string]any{{"asset_id": idA, "rendition": "main.avif", "ttl": 300}},
	}, scopedHdr), 200, "")

	// 10. GET /v1/namespaces/{ns}/usage & PUT /v1/namespaces/{ns}/quota.
	expect(t, e.call("GET", "/v1/namespaces/org_b/usage", nil, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("PUT", "/v1/namespaces/org_b/quota", map[string]any{"quota_bytes": 1000}, scopedHdr), 403, pipeline.CodeNamespaceForbidden)
	expect(t, e.call("GET", "/v1/namespaces/org_a/usage", nil, scopedHdr), 200, "")
	expect(t, e.call("PUT", "/v1/namespaces/org_a/quota", map[string]any{"quota_bytes": 100 << 20}, scopedHdr), 200, "")

	// Delete srvA cleanly with scoped key.
	expect(t, e.call("DELETE", "/v1/assets/"+srvA["id"].(string), nil, scopedHdr), 204, "")
}
