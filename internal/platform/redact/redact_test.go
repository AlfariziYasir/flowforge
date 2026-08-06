package redact_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/platform/redact"
)

const masked = redact.MaskValue

func redactString(t *testing.T, in string) string {
	t.Helper()
	out := redact.RedactPayload(json.RawMessage(in))
	return string(out)
}

func TestRedactPayload_TopLevelSensitiveKeys(t *testing.T) {
	for _, key := range []string{
		"authorization", "Authorization", "AUTHORIZATION",
		"api_key", "apiKey", "API-KEY",
		"password", "token", "secret",
		"access_token", "refresh_token", "x-api-key", "client_secret", "private_key",
	} {
		t.Run(key, func(t *testing.T) {
			in, err := json.Marshal(map[string]string{key: "super-secret-value"})
			require.NoError(t, err)

			out := redact.RedactPayload(in)

			var got map[string]string
			require.NoError(t, json.Unmarshal(out, &got))
			assert.Equal(t, masked, got[key])
			assert.NotContains(t, string(out), "super-secret-value")
		})
	}
}

func TestRedactPayload_ExactKeyMatch_NotSubstring(t *testing.T) {
	// "authorName" must survive untouched even though it contains "author" —
	// a substring match here would over-redact ordinary business data.
	out := redactString(t, `{"authorName": "Jane Doe", "authorization": "Bearer xyz"}`)

	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "Jane Doe", got["authorName"])
	assert.Equal(t, masked, got["authorization"])
}

func TestRedactPayload_NestedObjects(t *testing.T) {
	out := redactString(t, `{
		"step": {
			"request": {
				"headers": {"Authorization": "Bearer abc123", "Content-Type": "application/json"},
				"body": {"password": "hunter2", "username": "alice"}
			}
		}
	}`)

	assert.NotContains(t, out, "abc123")
	assert.NotContains(t, out, "hunter2")
	assert.Contains(t, out, "application/json")
	assert.Contains(t, out, "alice")
}

func TestRedactPayload_ArraysOfObjects(t *testing.T) {
	out := redactString(t, `{
		"logs": [
			{"message": "ok", "token": "secret-1"},
			{"message": "ok", "token": "secret-2"}
		]
	}`)

	assert.NotContains(t, out, "secret-1")
	assert.NotContains(t, out, "secret-2")
	assert.Contains(t, out, `"message":"ok"`)
}

func TestRedactPayload_NonObjectArrayLeavesUntouched(t *testing.T) {
	for _, in := range []string{`"just a string"`, `42`, `true`, `null`, `[1,2,3]`} {
		out := redactString(t, in)
		assert.JSONEq(t, in, out)
	}
}

func TestRedactPayload_EmptyOrNilInput(t *testing.T) {
	assert.Equal(t, json.RawMessage(`{}`), redact.RedactPayload(nil))
	assert.Equal(t, json.RawMessage(`{}`), redact.RedactPayload(json.RawMessage(``)))
}

func TestRedactPayload_InvalidJSONReturnsSafeMarker(t *testing.T) {
	// Malformed input must never be echoed back verbatim — it may itself carry
	// an unredacted secret from an upstream bug.
	out := redact.RedactPayload(json.RawMessage(`{not valid json`))
	assert.Equal(t, json.RawMessage(`"`+redact.UnredactableMarker+`"`), out)
}
