package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// cmdMCPBridge relays MCP JSON-RPC between stdio and Kadr's /mcp endpoint,
// for MCP clients that only launch local (stdio) servers:
//
//	KADR_AGENT_TOKEN=kd_agent_… kadr mcp-bridge --url https://admin.example.com/mcp
//
// Cloudflare Access service tokens can be passed with --cf-client-id /
// --cf-client-secret (or CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET).
func cmdMCPBridge(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("mcp-bridge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	endpoint := fs.String("url", os.Getenv("KADR_MCP_URL"), "Kadr MCP endpoint (https://…/mcp)")
	tokenEnv := fs.String("token-env", "KADR_AGENT_TOKEN", "environment variable holding the agent token")
	cfID := fs.String("cf-client-id", os.Getenv("CF_ACCESS_CLIENT_ID"), "Cloudflare Access service token id (optional)")
	cfSecret := fs.String("cf-client-secret", os.Getenv("CF_ACCESS_CLIENT_SECRET"), "Cloudflare Access service token secret (optional)")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if *endpoint == "" || token == "" {
		fmt.Fprintf(stderr, "mcp-bridge: set --url (or KADR_MCP_URL) and %s\n", *tokenEnv)
		return errUsage
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	out := bufio.NewWriter(stdout)
	protocol := ""
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var probe struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		_ = json.Unmarshal(line, &probe)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, *endpoint, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		if protocol != "" {
			req.Header.Set("MCP-Protocol-Version", protocol)
		}
		if *cfID != "" {
			req.Header.Set("CF-Access-Client-Id", *cfID)
			req.Header.Set("CF-Access-Client-Secret", *cfSecret)
		}
		resp, err := client.Do(req)
		var body []byte
		if err == nil {
			body, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			resp.Body.Close()
		}
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			body = bridgeError(probe.ID, "kadr unreachable: "+err.Error())
		} else if resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0 {
			continue // notification acknowledged
		} else if resp.StatusCode >= 400 && !json.Valid(body) {
			body = bridgeError(probe.ID, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
		} else if resp.StatusCode >= 400 {
			var rpc struct {
				JSONRPC string `json:"jsonrpc"`
			}
			if json.Unmarshal(body, &rpc) != nil || rpc.JSONRPC == "" { // a problem+json body, not JSON-RPC
				body = bridgeError(probe.ID, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
			}
		}
		if probe.Method == "initialize" {
			var r struct {
				Result struct {
					ProtocolVersion string `json:"protocolVersion"`
				} `json:"result"`
			}
			if json.Unmarshal(body, &r) == nil {
				protocol = r.Result.ProtocolVersion
			}
		}
		if len(probe.ID) == 0 { // never answer a notification
			continue
		}
		out.Write(bytes.TrimSpace(body))
		out.WriteByte('\n')
		if err := out.Flush(); err != nil {
			return err
		}
	}
	return sc.Err()
}

func bridgeError(id json.RawMessage, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": msg}})
	return b
}
