package edgepublish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublish(t *testing.T) {
	var gotPath, gotAuth string
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Write([]byte(`{"success":true,"errors":[]}`))
	}))
	defer srv.Close()
	p := &Publisher{AccountID: "acc", APIToken: "tok", BaseURL: srv.URL}
	if err := p.Publish(context.Background(), map[string]string{"k1": "s1"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/acc/workers/scripts/kadr-edge/secrets" || gotAuth != "Bearer tok" || got["name"] != "SIGNING_KEYS" || got["text"] != `{"k1":"s1"}` {
		t.Fatalf("%s %s %v", gotPath, gotAuth, got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"success":false,"errors":[{"message":"nope"}]}`))
	}))
	defer bad.Close()
	err := (&Publisher{AccountID: "a", APIToken: "tok", BaseURL: bad.URL}).Publish(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "nope") || strings.Contains(err.Error(), "tok") {
		t.Fatalf("%v", err)
	}
	if (&Publisher{}).Configured() {
		t.Fatal("empty publisher configured")
	}
}
