package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"flowforge/internal/engine"
)

// httpConfig is the HTTP node's config shape.
type httpConfig struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// HTTP executes an outbound request. The client passed in Input.HTTP is the
// SSRF-pinned client (internal/platform/safehttp): validation happens after DNS
// resolution and on every redirect hop. The response body is capped at
// MaxBodyBytes — an oversized body fails the step instead of buffering it all.
type HTTP struct{}

func (HTTP) Type() string { return "HTTP" }

func (HTTP) Execute(ctx context.Context, in Input) (Output, error) {
	var cfg httpConfig
	if err := json.Unmarshal(in.Node.Config, &cfg); err != nil {
		return Output{}, fmt.Errorf("http: invalid config: %w", err)
	}

	url, err := engine.Interpolate(cfg.URL, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("http: url: %w", err)
	}
	if url == "" {
		return Output{}, fmt.Errorf("http: url is empty")
	}

	body, err := engine.Interpolate(cfg.Body, in.Scope)
	if err != nil {
		return Output{}, fmt.Errorf("http: body: %w", err)
	}

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBufferString(body))
	if err != nil {
		return Output{}, fmt.Errorf("http: build request: %w", err)
	}
	for k, v := range cfg.Headers {
		hv, err := engine.Interpolate(v, in.Scope)
		if err != nil {
			return Output{}, fmt.Errorf("http: header %q: %w", k, err)
		}
		req.Header.Set(k, hv)
	}

	if in.HTTP == nil {
		return Output{}, fmt.Errorf("http: no SSRF-pinned client configured")
	}
	resp, err := in.HTTP.Do(req)
	if err != nil {
		return Output{}, fmt.Errorf("http: request: %w", err)
	}
	defer resp.Body.Close()

	limit := in.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20 // 1 MiB default
	}
	// Read at most limit+1 bytes: if we exceed limit the body is oversized and
	// the step fails without ever buffering the whole response.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Output{}, fmt.Errorf("http: read body: %w", err)
	}
	if int64(len(raw)) > limit {
		return Output{}, fmt.Errorf("http: response body exceeds %d bytes", limit)
	}

	output := map[string]any{
		"statusCode": resp.StatusCode,
		"status":     resp.Status,
		"body":       string(raw),
	}
	return Output{Data: output}, nil
}
