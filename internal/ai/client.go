// Package ai wraps the DeepSeek API for low-cost, low-latency thread
// classification. Reasoning is disabled (thinking: {type:"disabled"}) so a
// batch of dozens of threads classifies in a couple of seconds at minimal token
// cost.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jokull/sift/internal/config"
)

// Client talks to the DeepSeek OpenAI-compatible chat completions endpoint.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// New builds a client from config.
func New(cfg config.DeepSeekConfig) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// completion is the minimal chat completion response shape.
type completion struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// CompleteJSON performs a chat completion constrained to a JSON object, decoding
// the assistant's content into out. system and user are the prompt halves.
func (c *Client) CompleteJSON(ctx context.Context, system, user string, out any) error {
	payload := map[string]any{
		"model":           c.model,
		"temperature":     0,
		"max_tokens":      8192,
		"thinking":        map[string]any{"type": "disabled"},
		"response_format": map[string]any{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deepseek %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var comp completion
	if err := json.Unmarshal(body, &comp); err != nil {
		return fmt.Errorf("decode deepseek response: %w", err)
	}
	if comp.Error != nil {
		return fmt.Errorf("deepseek error: %s", comp.Error.Message)
	}
	if len(comp.Choices) == 0 {
		return fmt.Errorf("deepseek returned no choices")
	}
	content := comp.Choices[0].Message.Content
	// Strip any markdown fence that may wrap the JSON.
	content = strings.TrimSpace(content)
	if i := strings.Index(content, "{"); i > 0 {
		content = content[i:]
	}
	if j := strings.LastIndex(content, "}"); j >= 0 {
		content = content[:j+1]
	}
	if err := json.Unmarshal([]byte(content), out); err != nil {
		return fmt.Errorf("decode deepseek json content: %w\n%s", err, truncate(content, 300))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
