// Package edgepublish pushes signing keys to the kadr-edge Worker as the
// SIGNING_KEYS secret through the Cloudflare API.
package edgepublish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Publisher updates the Worker secret.
type Publisher struct {
	AccountID  string
	APIToken   string
	WorkerName string // default "kadr-edge"
	BaseURL    string // default https://api.cloudflare.com/client/v4
	Client     *http.Client
}

// Configured reports whether credentials are present.
func (p *Publisher) Configured() bool { return p != nil && p.AccountID != "" && p.APIToken != "" }

// Publish sets SIGNING_KEYS to the JSON map kid → secret.
func (p *Publisher) Publish(ctx context.Context, keys map[string]string) error {
	if !p.Configured() {
		return fmt.Errorf("edgepublish: KADR_CF_ACCOUNT_ID and KADR_CF_API_TOKEN are not set")
	}
	value, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"name": "SIGNING_KEYS", "text": string(value), "type": "secret_text"})
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	worker := p.WorkerName
	if worker == "" {
		worker = "kadr-edge"
	}
	url := fmt.Sprintf("%s/accounts/%s/workers/scripts/%s/secrets", base, p.AccountID, worker)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIToken)
	req.Header.Set("Content-Type", "application/json")
	c := p.Client
	if c == nil {
		c = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("edgepublish: %s", strings.ReplaceAll(err.Error(), p.APIToken, "***"))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode/100 != 2 || !out.Success {
		msg := ""
		if len(out.Errors) > 0 {
			msg = out.Errors[0].Message
		}
		return fmt.Errorf("edgepublish: Cloudflare answered HTTP %d %s", resp.StatusCode, msg)
	}
	return nil
}
