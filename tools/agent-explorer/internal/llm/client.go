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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("request llm: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, string(respBody))
	}

	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(respBody), []byte("data:")) {
		return parseStreamingResponse(respBody)
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse llm response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
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
