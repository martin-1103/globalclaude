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

	"agent-db/internal/config"
	"agent-db/internal/logs"
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

// Chat sends the full message history (system + alternating user/assistant)
// and returns the assistant's next reply. The agentic loop owns the history.
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	start := time.Now()
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.1,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"body": logs.Preview(string(respBody), 200)}, resp.StatusCode, fmt.Errorf("llm status %d", resp.StatusCode), time.Since(start).Milliseconds())
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, string(respBody))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(respBody), []byte("data:")) {
		out, err := parseStreamingResponse(respBody)
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"content": logs.Preview(out, 200), "stream": true}, resp.StatusCode, err, time.Since(start).Milliseconds())
		return out, err
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"body": logs.Preview(string(respBody), 200)}, resp.StatusCode, err, time.Since(start).Milliseconds())
		return "", fmt.Errorf("parse llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		appendLLMLog(ctx, c.model, reqBody, map[string]any{"choices": 0}, resp.StatusCode, fmt.Errorf("llm returned no choices"), time.Since(start).Milliseconds())
		return "", fmt.Errorf("llm returned no choices")
	}
	out := strings.TrimSpace(parsed.Choices[0].Message.Content)
	appendLLMLog(ctx, c.model, reqBody, map[string]any{"content": logs.Preview(out, 200)}, resp.StatusCode, nil, time.Since(start).Milliseconds())
	return out, nil
}

func appendLLMLog(ctx context.Context, model string, req chatRequest, resp map[string]any, status int, err error, durationMs int64) {
	logger := logs.LLMLoggerFromContext(ctx)
	if logger == nil {
		return
	}
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]any{
			"role":    m.Role,
			"content": logs.Preview(m.Content, 140),
		})
	}
	entry := logs.LLMEntry{
		Model:      model,
		Request:    map[string]any{"messages": msgs, "temperature": req.Temperature},
		Response:   resp,
		HTTPStatus: status,
		DurationMs: durationMs,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	_ = logger.Append(entry)
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
