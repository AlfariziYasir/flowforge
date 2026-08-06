package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrProviderNotConfigured is returned when no API key is set. The provider
// fails closed: it never sends an unauthenticated request to find out.
var ErrProviderNotConfigured = errors.New("ai provider: no API key configured")

// ErrGenerationFailed wraps any network error, non-2xx response, or malformed
// response body — anything that means "the model call didn't produce usable
// output", collapsed to one sentinel so callers don't need to distinguish a
// timeout from a 500 from a truncated body; they all mean the same thing to a
// caller deciding whether to retry or fail the request.
var ErrGenerationFailed = errors.New("ai provider: generation failed")

// Config configures the DeepSeek client. Model is left to the caller rather
// than hardcoded: DeepSeek's currently-priced models are named
// "deepseek-v4-flash"/"deepseek-v4-pro" as of writing, while "deepseek-chat" is
// the long-stable alias — pinning one in code risks staleness as the vendor's
// lineup shifts.
type Config struct {
	APIKey         string
	Model          string
	BaseURL        string // e.g. https://api.deepseek.com
	RequestTimeout time.Duration
}

type deepSeekProvider struct {
	cfg    Config
	client *http.Client
}

// NewDeepSeekProvider builds a Provider backed by DeepSeek's OpenAI-compatible
// chat completions endpoint (POST {BaseURL}/chat/completions).
func NewDeepSeekProvider(cfg Config) Provider {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	return &deepSeekProvider{
		cfg:    cfg,
		client: &http.Client{},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Complete sends a two-message chat completion request in DeepSeek's JSON
// mode. JSON mode is not a strict schema enforcement — DeepSeek's own docs
// state the model must be instructed to emit JSON via the prompt, and the
// caller must still validate the result. That validation belongs to
// internal/execution, not here: this client only speaks HTTP.
//
// The provider enforces its own RequestTimeout regardless of the caller's ctx,
// so a caller that passes context.Background() by mistake still gets bounded.
// An already-cancelled or exceeded caller ctx also surfaces as
// ErrGenerationFailed — callers do not need to distinguish "the model was slow"
// from "I gave up waiting".
func (p *deepSeekProvider) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if p.cfg.APIKey == "" {
		return "", ErrProviderNotConfigured
	}

	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.RequestTimeout)
	defer cancel()

	body := chatCompletionRequest{
		Model: p.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	}
	body.ResponseFormat.Type = "json_object"

	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("%w: encode request: %v", ErrGenerationFailed, err)
	}

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		p.cfg.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrGenerationFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGenerationFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: read response: %v", ErrGenerationFailed, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: status %d", ErrGenerationFailed, resp.StatusCode)
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("%w: decode response: %v", ErrGenerationFailed, err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("%w: no choices in response", ErrGenerationFailed)
	}

	return parsed.Choices[0].Message.Content, nil
}
