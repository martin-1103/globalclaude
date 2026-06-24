package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-explorer/internal/audit"
	"agent-explorer/internal/config"
)

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

func New(cfg config.Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		model:   cfg.Model,
		http: &http.Client{
			Timeout: time.Duration(cfg.LLMTimeoutSeconds) * time.Second,
		},
	}
}

func (c *Client) Chat(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	const maxAttempts = 3
	backoff := []time.Duration{time.Second, 3 * time.Second}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		out, err := c.chatOnce(ctx, systemPrompt, userPrompt, attempt)
		if err == nil {
			return out, nil
		}
		lastErr = err

		errStr := err.Error()
		if !strings.Contains(errStr, "timeout") && !strings.Contains(errStr, "deadline exceeded") && !strings.Contains(errStr, "context deadline") {
			return "", err
		}

		if attempt < maxAttempts-1 {
			time.Sleep(backoff[attempt])
		}
	}
	return "", fmt.Errorf("llm timeout after %d attempts: %w", maxAttempts, lastErr)
}

func (c *Client) chatOnce(ctx context.Context, systemPrompt string, userPrompt string, attempt int) (string, error) {
	start := time.Now()
	reqBody := chatRequest{
		Model: c.model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.1,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Detach from parent context deadline — LLM call gets its own 30s budget
	// so it cannot consume the parent timeout and starve downstream tools.
	detached := context.WithoutCancel(ctx)
	reqCtx, cancel := context.WithTimeout(detached, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		appendLLMLog(ctx, c.model, reqBody, nil, 0, err, time.Since(start).Milliseconds())
		return "", fmt.Errorf("request llm: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		appendLLMLog(ctx, c.model, reqBody, nil, resp.StatusCode, err, time.Since(start).Milliseconds())
		return "", fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode >= 300 {
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"body": audit.Redact(string(respBody), 1000)}, resp.StatusCode, fmt.Errorf("llm status %d", resp.StatusCode), time.Since(start).Milliseconds())
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, string(respBody))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(respBody), []byte("data:")) {
		out, err := parseStreamingResponse(respBody)
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"content": audit.Redact(out, 1000), "stream": true}, resp.StatusCode, err, time.Since(start).Milliseconds())
		return out, err
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"body": audit.Redact(string(respBody), 1000)}, resp.StatusCode, err, time.Since(start).Milliseconds())
		return "", fmt.Errorf("parse llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"choices": 0}, resp.StatusCode, fmt.Errorf("llm returned no choices"), time.Since(start).Milliseconds())
		return "", fmt.Errorf("llm returned no choices")
	}
	out := strings.TrimSpace(parsed.Choices[0].Message.Content)
	appendLLMLog(ctx, c.model, reqBody, map[string]any{"content": audit.Redact(out, 1000)}, resp.StatusCode, nil, time.Since(start).Milliseconds())
	return out, nil
}

func appendLLMLog(ctx context.Context, model string, req chatRequest, resp map[string]any, status int, err error, durationMs int64) {
	logger := audit.FromContext(ctx)
	if logger == nil {
		return
	}
	entry := audit.LLMEntry{
		Model:      model,
		Request:    map[string]any{"messages": previewMessages(req.Messages), "temperature": req.Temperature},
		Response:   resp,
		HTTPStatus: status,
		DurationMs: durationMs,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	_ = logger.Append(entry)
}

func previewMessages(items []Message) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"role":    item.Role,
			"content": audit.Preview(item.Content, 140),
		})
	}
	return out
}

func parseStreamingResponse(respBody []byte) (string, error) {
	lines := strings.Split(string(respBody), "\n")
	var builder strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, choice := range chunk.Choices {
			builder.WriteString(choice.Delta.Content)
		}
	}
	out := strings.TrimSpace(builder.String())
	if out == "" {
		return "", fmt.Errorf("llm stream returned no content")
	}
	return out, nil
}
