// Package mcp is a small, dependency-free server for the Model Context
// Protocol over the Streamable HTTP transport (stateless, JSON responses).
// It covers what Kadr exposes to AI agents: tools, resources and prompts.
//
// Spec: https://modelcontextprotocol.io/specification. The server answers
// every POST with a single JSON body (no SSE stream, no server-initiated
// messages), which the transport explicitly allows, and issues no session id.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Protocol versions this server speaks, newest first.
var SupportedVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// LatestVersion is offered when the client asks for an unknown version.
var LatestVersion = SupportedVersions[0]

// MaxBodyBytes bounds one request (tool arguments may carry a base64 image).
const MaxBodyBytes = 24 << 20

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Implementation identifies the server.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// ToolAnnotations are hints for clients (they never replace server checks).
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// Content is one block of a tool result or prompt message.
type Content struct {
	Type     string `json:"type"` // text | image
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"` // base64 (image)
	MimeType string `json:"mimeType,omitempty"`
}

// TextContent returns a text block.
func TextContent(s string) Content { return Content{Type: "text", Text: s} }

// ToolResult is the result of tools/call.
type ToolResult struct {
	Content           []Content `json:"content"`
	StructuredContent any       `json:"structuredContent,omitempty"`
	IsError           bool      `json:"isError,omitempty"`
}

// ErrorResult is a tool-level failure the model should see and react to.
func ErrorResult(format string, args ...any) *ToolResult {
	return &ToolResult{Content: []Content{TextContent(fmt.Sprintf(format, args...))}, IsError: true}
}

// Tool is a callable tool.
type Tool struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *ToolAnnotations `json:"annotations,omitempty"`
	// Call runs the tool. Returning an error yields a JSON-RPC internal error;
	// expected failures should be returned as ErrorResult instead.
	Call func(ctx context.Context, args map[string]any) (*ToolResult, error) `json:"-"`
}

// Resource is a readable document.
type Resource struct {
	URI         string                                    `json:"uri"`
	Name        string                                    `json:"name"`
	Title       string                                    `json:"title,omitempty"`
	Description string                                    `json:"description,omitempty"`
	MimeType    string                                    `json:"mimeType,omitempty"`
	Read        func(ctx context.Context) (string, error) `json:"-"`
}

// PromptArgument describes a prompt parameter.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage is one message of a rendered prompt.
type PromptMessage struct {
	Role    string  `json:"role"` // user | assistant
	Content Content `json:"content"`
}

// Prompt is a reusable task template.
type Prompt struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
	// Render returns the messages for the given arguments.
	Render func(args map[string]string) ([]PromptMessage, error) `json:"-"`
}

// Catalog is what one authenticated caller can see and use.
type Catalog struct {
	Instructions string
	Tools        []*Tool
	Resources    []*Resource
	Prompts      []*Prompt
}

// Handler serves MCP over Streamable HTTP.
type Handler struct {
	Info Implementation
	// Authenticate resolves the caller. On failure it writes the HTTP
	// response itself and returns ok=false.
	Authenticate func(w http.ResponseWriter, r *http.Request) (ctx context.Context, cat *Catalog, ok bool)
	// AllowedOrigins lists browser origins allowed to call the endpoint (DNS
	// rebinding protection). Requests without Origin are always allowed.
	AllowedOrigins []string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (h *Handler) originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	if slices.Contains(h.AllowedOrigins, o) {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// ServeHTTP implements the Streamable HTTP transport (POST only).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		writeHTTPError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream is offered (GET) and there is no
		// session to terminate (DELETE).
		w.Header().Set("Allow", "POST")
		writeHTTPError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(SupportedVersions, v) {
		writeHTTPError(w, http.StatusBadRequest, "unsupported MCP-Protocol-Version "+v)
		return
	}
	ctx, cat, ok := h.Authenticate(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		writeHTTPError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) > 0 && body[0] == '[' { // batch (2025-03-26)
		var msgs []json.RawMessage
		if err := json.Unmarshal(body, &msgs); err != nil || len(msgs) == 0 {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: CodeParseError, Message: "parse error"}})
			return
		}
		var out []rpcResponse
		for _, m := range msgs {
			if resp, isReq := h.handleMessage(ctx, cat, m); isReq {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, out)
		return
	}
	resp, isReq := h.handleMessage(ctx, cat, body)
	if !isReq {
		w.WriteHeader(http.StatusAccepted) // notification or response from the client
		return
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeHTTPError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
		Error: &rpcError{Code: CodeInvalidRequest, Message: msg}})
}

// handleMessage processes one JSON-RPC message. isReq is false for
// notifications and client responses (nothing to answer).
func (h *Handler) handleMessage(ctx context.Context, cat *Catalog, raw json.RawMessage) (rpcResponse, bool) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: CodeParseError, Message: "parse error"}}, true
	}
	if req.Method == "" { // a response to a server request (we never send any)
		return rpcResponse{}, false
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return rpcResponse{}, false // notification (initialized, cancelled, …)
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if req.JSONRPC != "2.0" {
		resp.Error = &rpcError{Code: CodeInvalidRequest, Message: `jsonrpc must be "2.0"`}
		return resp, true
	}
	result, rerr := h.dispatch(ctx, cat, req.Method, req.Params)
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp, true
}

func invalidParams(msg string) *rpcError { return &rpcError{Code: CodeInvalidParams, Message: msg} }

func (h *Handler) dispatch(ctx context.Context, cat *Catalog, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(params, &p)
		v := p.ProtocolVersion
		if !slices.Contains(SupportedVersions, v) {
			v = LatestVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
				"prompts":   map[string]any{"listChanged": false},
			},
			"serverInfo":   h.Info,
			"instructions": cat.Instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := cat.Tools
		if tools == nil {
			tools = []*Tool{}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams("invalid tools/call params")
		}
		for _, t := range cat.Tools {
			if t.Name != p.Name {
				continue
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			if msg := checkRequired(t.InputSchema, p.Arguments); msg != "" {
				return ErrorResult("%s", msg), nil
			}
			res, err := t.Call(ctx, p.Arguments)
			if err != nil {
				return nil, &rpcError{Code: CodeInternalError, Message: "tool failed: " + err.Error()}
			}
			if res.Content == nil {
				res.Content = []Content{}
			}
			return res, nil
		}
		return nil, invalidParams("unknown tool: " + p.Name + " (it may need permissions this token lacks; call kadr_whoami)")
	case "resources/list":
		rs := cat.Resources
		if rs == nil {
			rs = []*Resource{}
		}
		return map[string]any{"resources": rs}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.URI == "" {
			return nil, invalidParams("uri is required")
		}
		for _, res := range cat.Resources {
			if res.URI == p.URI {
				text, err := res.Read(ctx)
				if err != nil {
					return nil, &rpcError{Code: CodeInternalError, Message: err.Error()}
				}
				return map[string]any{"contents": []map[string]any{{"uri": res.URI, "mimeType": res.MimeType, "text": text}}}, nil
			}
		}
		return nil, &rpcError{Code: -32002, Message: "resource not found", Data: map[string]string{"uri": p.URI}}
	case "prompts/list":
		ps := cat.Prompts
		if ps == nil {
			ps = []*Prompt{}
		}
		return map[string]any{"prompts": ps}, nil
	case "prompts/get":
		var p struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, invalidParams("invalid prompts/get params")
		}
		for _, pr := range cat.Prompts {
			if pr.Name != p.Name {
				continue
			}
			for _, a := range pr.Arguments {
				if a.Required && strings.TrimSpace(p.Arguments[a.Name]) == "" {
					return nil, invalidParams("missing required argument " + a.Name)
				}
			}
			msgs, err := pr.Render(p.Arguments)
			if err != nil {
				return nil, invalidParams(err.Error())
			}
			return map[string]any{"description": pr.Description, "messages": msgs}, nil
		}
		return nil, invalidParams("unknown prompt: " + p.Name)
	case "logging/setLevel":
		return map[string]any{}, nil
	}
	return nil, &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

// checkRequired verifies the schema's required properties are present.
func checkRequired(schema map[string]any, args map[string]any) string {
	req, _ := schema["required"].([]string)
	var missing []string
	for _, k := range req {
		v, ok := args[k]
		if !ok || v == nil {
			missing = append(missing, k)
			continue
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return "missing required argument(s): " + strings.Join(missing, ", ")
	}
	return ""
}
