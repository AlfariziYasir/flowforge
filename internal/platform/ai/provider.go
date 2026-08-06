// Package ai provides a provider-agnostic LLM client for run-failure analysis
// (internal/execution's AnalyzeRun). The concrete client is DeepSeek, but every
// caller depends only on Provider, so swapping providers later is a single new
// file, not a redesign.
package ai

import "context"

// Provider completes a chat-style prompt and returns the raw response text.
// It does not parse or validate the response — that is the caller's job, since
// the expected schema (diagnosis/possibleCause/suggestedFix/confidence) is
// execution-specific, not provider-specific.
type Provider interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}
