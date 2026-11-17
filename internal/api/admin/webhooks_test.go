package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/UdjinStolboviy/kadr-madia/internal/webhook"
)

func TestWebhookEndpointsAndDeliveries(t *testing.T) {
	e := newEnv(t, nil)
	c := &client{e: e}
	if r := c.login(t, nil); r.code != 200 {
		t.Fatalf("login: %d %s", r.code, r.body)
	}
	r := c.do("POST", "/admin/api/projects", map[string]any{"slug": "acme", "name": "Acme", "environment": "live"}, true)
	if r.code != 201 {
		t.Fatalf("create project: %d %s", r.code, r.body)
	}
	pid := r.json(t)["id"].(string)

	var lastSig, lastID, lastTS string
	var lastBody []byte
	receiverStatus := http.StatusInternalServerError
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		lastSig, lastID, lastTS = r.Header.Get("webhook-signature"), r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp")
		w.WriteHeader(receiverStatus)
	}))
	defer recv.Close()

	expectProblem(t, c.do("POST", "/admin/api/projects/"+pid+"/webhooks", map[string]any{"url": "ftp://x"}, true), 422, "validation_failed")
	expectProblem(t, c.do("POST", "/admin/api/projects/"+pid+"/webhooks", map[string]any{"url": recv.URL, "events": []string{"asset.nope"}}, true), 422, "validation_failed")

	r = c.do("POST", "/admin/api/projects/"+pid+"/webhooks", map[string]any{"url": recv.URL, "events": []string{"asset.*"}}, true)
	if r.code != 201 {
		t.Fatalf("create webhook: %d %s", r.code, r.body)
	}
	body := r.json(t)
	secret := body["secret"].(string)
	hook := body["webhook"].(map[string]any)
	hid := hook["id"].(string)
	if !strings.HasPrefix(secret, "whsec_") || hook["url"] != recv.URL || hook["events"].([]any)[0] != "asset.*" {
		t.Fatalf("created webhook: %s", r.body)
	}
	r = c.do("GET", "/admin/api/projects/"+pid+"/webhooks", nil, false)
	if items := r.json(t)["items"].([]any); len(items) != 1 || strings.Contains(string(r.body), secret) {
		t.Fatalf("list webhooks leaks or misses: %s", r.body)
	}

	// Test delivery: the receiver fails first, so the delivery is pending.
	r = c.do("POST", "/admin/api/webhooks/"+hid+"/test", nil, true)
	if r.code != 200 {
		t.Fatalf("test webhook: %d %s", r.code, r.body)
	}
	d := r.json(t)
	if d["status"] != "pending" || d["attempts"] != float64(1) || d["last_status_code"] != float64(500) || d["event_type"] != "webhook.test" {
		t.Fatalf("test delivery: %s", r.body)
	}
	if err := webhook.Verify(secret, lastID, lastTS, lastSig, lastBody, time.Now(), 0); err != nil {
		t.Fatalf("test webhook signature: %v", err)
	}

	r = c.do("GET", "/admin/api/projects/"+pid+"/deliveries?status=pending", nil, false)
	if items := r.json(t)["items"].([]any); len(items) != 1 {
		t.Fatalf("pending deliveries: %s", r.body)
	}
	expectProblem(t, c.do("GET", "/admin/api/projects/"+pid+"/deliveries?status=bogus", nil, false), 422, "validation_failed")

	// Manual retry resets the attempt budget; the dispatcher delivers it.
	receiverStatus = http.StatusOK
	r = c.do("POST", "/admin/api/deliveries/"+d["id"].(string)+"/retry", nil, true)
	if r.code != 200 || r.json(t)["attempts"] != float64(0) || r.json(t)["status"] != "pending" {
		t.Fatalf("retry: %d %s", r.code, r.body)
	}
	if !e.webhooks.ProcessOne(context.Background()) {
		t.Fatal("retried delivery was not due")
	}
	r = c.do("GET", "/admin/api/projects/"+pid+"/deliveries?status=delivered", nil, false)
	if items := r.json(t)["items"].([]any); len(items) != 1 {
		t.Fatalf("delivered: %s", r.body)
	}
	expectProblem(t, c.do("POST", "/admin/api/deliveries/whd_missing/retry", nil, true), 404, "not_found")

	if r := c.do("DELETE", "/admin/api/webhooks/"+hid, nil, true); r.code != 204 {
		t.Fatalf("delete webhook: %d %s", r.code, r.body)
	}
	expectProblem(t, c.do("POST", "/admin/api/webhooks/"+hid+"/test", nil, true), 404, "not_found")

	actions := map[string]bool{}
	for _, it := range c.do("GET", "/admin/api/audit?limit=200", nil, false).json(t)["items"].([]any) {
		actions[it.(map[string]any)["action"].(string)] = true
	}
	for _, a := range []string{"webhook.create", "webhook.test", "delivery.retry", "webhook.delete"} {
		if !actions[a] {
			t.Fatalf("missing audit action %s", a)
		}
	}
}
