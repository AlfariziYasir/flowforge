package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/platform/ai"
)

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

func chatCompletionResponse(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"},
		},
	}
}

func TestDeepSeekProvider_Complete_SendsCorrectRequestShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody chatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(chatCompletionResponse(`{"diagnosis":"ok"}`))
	}))
	defer srv.Close()

	p := ai.NewDeepSeekProvider(ai.Config{
		APIKey:         "sk-test-key",
		Model:          "deepseek-chat",
		BaseURL:        srv.URL,
		RequestTimeout: 5 * time.Second,
	})

	out, err := p.Complete(context.Background(), "you are a diagnostician", "the run failed")
	require.NoError(t, err)
	assert.Equal(t, `{"diagnosis":"ok"}`, out)

	assert.Equal(t, "/chat/completions", gotPath)
	assert.Equal(t, "Bearer sk-test-key", gotAuth)
	assert.Equal(t, "deepseek-chat", gotBody.Model)
	assert.Equal(t, "json_object", gotBody.ResponseFormat.Type)
	require.Len(t, gotBody.Messages, 2)
	assert.Equal(t, "system", gotBody.Messages[0].Role)
	assert.Equal(t, "you are a diagnostician", gotBody.Messages[0].Content)
	assert.Equal(t, "user", gotBody.Messages[1].Role)
	assert.Equal(t, "the run failed", gotBody.Messages[1].Content)
}

func TestDeepSeekProvider_Complete_EmptyAPIKeyFailsClosed(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	p := ai.NewDeepSeekProvider(ai.Config{
		APIKey:         "",
		Model:          "deepseek-chat",
		BaseURL:        srv.URL,
		RequestTimeout: 5 * time.Second,
	})

	_, err := p.Complete(context.Background(), "sys", "user")
	require.Error(t, err)
	assert.ErrorIs(t, err, ai.ErrProviderNotConfigured)
	assert.False(t, called, "must never make a network call with no API key")
}

func TestDeepSeekProvider_Complete_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"upstream exploded"}`))
	}))
	defer srv.Close()

	p := ai.NewDeepSeekProvider(ai.Config{APIKey: "k", Model: "m", BaseURL: srv.URL, RequestTimeout: 5 * time.Second})

	_, err := p.Complete(context.Background(), "sys", "user")
	require.Error(t, err)
	assert.ErrorIs(t, err, ai.ErrGenerationFailed)
}

func TestDeepSeekProvider_Complete_MalformedResponseBody(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `not json at all`,
		"empty choices": `{"choices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer srv.Close()

			p := ai.NewDeepSeekProvider(ai.Config{APIKey: "k", Model: "m", BaseURL: srv.URL, RequestTimeout: 5 * time.Second})

			_, err := p.Complete(context.Background(), "sys", "user")
			require.Error(t, err)
			assert.ErrorIs(t, err, ai.ErrGenerationFailed)
		})
	}
}

func TestDeepSeekProvider_Complete_RequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	p := ai.NewDeepSeekProvider(ai.Config{
		APIKey:         "k",
		Model:          "m",
		BaseURL:        srv.URL,
		RequestTimeout: 20 * time.Millisecond,
	})

	start := time.Now()
	_, err := p.Complete(context.Background(), "sys", "user")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, ai.ErrGenerationFailed)
	assert.Less(t, elapsed, 200*time.Millisecond, "the provider's own timeout must fire before the slow handler responds")
}

func TestDeepSeekProvider_Complete_HonoursCallerCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	p := ai.NewDeepSeekProvider(ai.Config{APIKey: "k", Model: "m", BaseURL: srv.URL, RequestTimeout: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Complete(ctx, "sys", "user")
	require.Error(t, err)
	assert.ErrorIs(t, err, ai.ErrGenerationFailed)
}
