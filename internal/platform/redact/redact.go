// Package redact strips sensitive values out of arbitrary JSON payloads before
// they leave the process boundary — specifically, before step input/output/error
// payloads and log context are sent to an LLM provider for run analysis
// (internal/execution's AnalyzeRun). It is a distinct responsibility from
// internal/platform/logger's RedactURL, which only masks a connection string's
// userinfo password.
package redact

import "encoding/json"

// MaskValue replaces a redacted field's value.
const MaskValue = "***REDACTED***"

// UnredactableMarker is returned when the input cannot be parsed as JSON at
// all. Malformed input must never be echoed back verbatim — it may itself carry
// an unredacted secret from whatever produced it.
const UnredactableMarker = "unredactable payload"

// sensitiveKeys is compared against a normalized form of each object key
// (lowercased, with '-' and '_' stripped), so "api_key", "apiKey", and
// "API-KEY" are all treated as the same key. Comparison is against the whole
// normalized key, never a substring — "authorName" must never be redacted
// just because it contains "author".
var sensitiveKeys = map[string]struct{}{
	"authorization": {},
	"apikey":        {},
	"password":      {},
	"token":         {},
	"secret":        {},
	"accesstoken":   {},
	"refreshtoken":  {},
	"xapikey":       {},
	"clientsecret":  {},
	"privatekey":    {},
}

func normalizeKey(key string) string {
	out := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '-' || c == '_':
			continue
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func isSensitiveKey(key string) bool {
	_, ok := sensitiveKeys[normalizeKey(key)]
	return ok
}

// RedactPayload parses raw as JSON and returns an equivalent document with
// every value under a sensitive key masked, at any nesting depth, inside
// objects and arrays alike. Non-object/array leaves and unrecognized key names
// pass through unchanged. Key order is not preserved — irrelevant for this
// package's only consumer, an LLM prompt.
//
// A nil or empty input returns "{}". Input that fails to parse as JSON returns
// UnredactableMarker rather than the original bytes, so a malformed upstream
// payload can never leak verbatim.
func RedactPayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		marker, _ := json.Marshal(UnredactableMarker)
		return json.RawMessage(marker)
	}

	redacted := redactValue(v)

	out, err := json.Marshal(redacted)
	if err != nil {
		marker, _ := json.Marshal(UnredactableMarker)
		return json.RawMessage(marker)
	}
	return out
}

func redactValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, child := range val {
			if isSensitiveKey(k) {
				out[k] = MaskValue
				continue
			}
			out[k] = redactValue(child)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, child := range val {
			out[i] = redactValue(child)
		}
		return out
	default:
		return val
	}
}
