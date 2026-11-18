package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

func msg(content []map[string]any, stop string) map[string]any {
	return map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content": content, "stop_reason": stop, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
}

type fake struct {
	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
	replies  []map[string]any
}

func (f *fake) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		f.requests = append(f.requests, req)
		f.headers = append(f.headers, r.Header.Clone())
		if len(f.replies) == 0 {
			t.Errorf("unexpected request %s", b)
			w.WriteHeader(500)
			return
		}
		rep := f.replies[0]
		f.replies = f.replies[1:]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rep)
	}))
}

func TestClassify(t *testing.T) {
	f := &fake{replies: []map[string]any{
		msg([]map[string]any{{"type": "text", "text": `{"status":"rejected","labels":["NSFW","nsfw"," gore "],"note":"explicit","confidence":0.97}`}}, "end_turn"),
		msg([]map[string]any{}, "refusal"),
	}}
	srv := f.server(t)
	defer srv.Close()
	c := New("k", "", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	d, err := c.Classify(context.Background(), []byte("img"), "image/webp", "no nudity")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "rejected" || len(d.Labels) != 2 || d.Labels[1] != "gore" {
		t.Fatalf("decision %+v", d)
	}
	req := f.requests[0]
	if req["model"] != "claude-opus-5-5" || req["fallbacks"] != "default" {
		t.Fatalf("model/fallbacks: %v", req)
	}
	oc := req["output_config"].(map[string]any)
	if oc["effort"] != "low" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("output_config: %v", oc)
	}
	if !strings.Contains(f.headers[0].Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("beta header: %v", f.headers[0])
	}
	content := req["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["type"] != "image" {
		t.Fatalf("image block: %v", content)
	}
	d, err = c.Classify(context.Background(), []byte("img"), "image/webp", "p")
	if err != nil || d.Status != "flagged" || d.Labels[0] != "ai_refused" {
		t.Fatalf("refusal: %+v %v", d, err)
	}
	if _, err := c.Classify(context.Background(), []byte("x"), "image/avif", "p"); err == nil {
		t.Fatal("avif accepted")
	}
}

func TestRunAgent(t *testing.T) {
	f := &fake{replies: []map[string]any{
		msg([]map[string]any{{"type": "text", "text": "Checking."},
			{"type": "tool_use", "id": "tu_1", "name": "kadr_list_projects", "input": map[string]any{}},
			{"type": "tool_use", "id": "tu_2", "name": "kadr_view_asset", "input": map[string]any{"asset_id": "ast_1"}}}, "tool_use"),
		msg([]map[string]any{{"type": "text", "text": "All good: 1 project."}}, "end_turn"),
	}}
	srv := f.server(t)
	defer srv.Close()
	c := New("k", "claude-haiku-5-5", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	var calls []string
	tools := []Tool{
		{Name: "kadr_list_projects", Description: "list", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
			Call: func(_ context.Context, _ map[string]any) ToolOutput {
				calls = append(calls, "list")
				return ToolOutput{Text: `{"items":[1]}`}
			}},
		{Name: "kadr_view_asset", Description: "view", Schema: map[string]any{"type": "object", "properties": map[string]any{"asset_id": map[string]any{"type": "string"}}, "required": []string{"asset_id"}},
			Call: func(_ context.Context, a map[string]any) ToolOutput {
				calls = append(calls, "view:"+a["asset_id"].(string))
				return ToolOutput{Text: "preview", Images: []Image{{MimeType: "image/webp", Data: "AAAA"}}}
			}},
	}
	res, err := c.RunAgent(context.Background(), "system", "check health", tools, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "All good: 1 project." || res.ToolCalls != 2 || res.Turns != 2 || res.InputTokens != 20 || strings.Join(calls, ",") != "list,view:ast_1" {
		t.Fatalf("result %+v calls %v", res, calls)
	}
	if _, ok := f.requests[0]["fallbacks"]; ok {
		t.Fatal("fallbacks sent for a model without server-side fallback")
	}
	second := f.requests[1]["messages"].([]any)
	results := second[2].(map[string]any)["content"].([]any)
	if len(results) != 2 || results[1].(map[string]any)["content"].([]any)[1].(map[string]any)["type"] != "image" {
		t.Fatalf("tool results: %v", results)
	}
	if len(f.requests[0]["tools"].([]any)) != 2 {
		t.Fatalf("tools: %v", f.requests[0]["tools"])
	}
}

func TestHTTPClassifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["mime_type"] != "image/jpeg" || in["image_base64"] == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"status":"weird","labels":["nsfw"],"note":"n","confidence":0.6}`))
	}))
	defer srv.Close()
	d, err := (&HTTPClassifier{URL: srv.URL}).Classify(context.Background(), []byte("x"), "image/jpeg", "p")
	if err != nil || d.Status != "flagged" {
		t.Fatalf("%+v %v", d, err)
	}
}
