package httpmw_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"flowforge/internal/platform/httpmw"
)

func TestCORS(t *testing.T) {
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	t.Run("empty allowlist sets no CORS headers", func(t *testing.T) {
		handler := httpmw.CORS(nil, false)(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("allowed origin receives CORS headers without credentials", func(t *testing.T) {
		handler := httpmw.CORS([]string{"http://localhost:3000", "https://app.flowforge.dev"}, false)(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
		req.Header.Set("Origin", "https://app.flowforge.dev")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "https://app.flowforge.dev", rec.Header().Get("Access-Control-Allow-Origin"))
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))
		assert.NotEmpty(t, rec.Header().Get("Access-Control-Allow-Methods"))
		assert.NotEmpty(t, rec.Header().Get("Access-Control-Allow-Headers"))
	})

	t.Run("allowed origin with credentials enabled sets header and never emits wildcard", func(t *testing.T) {
		handler := httpmw.CORS([]string{"http://localhost:3000"}, true)(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
		assert.NotEqual(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
	})

	t.Run("preflight OPTIONS request from allowed origin returns 204 No Content", func(t *testing.T) {
		handler := httpmw.CORS([]string{"http://localhost:3000"}, true)(dummyHandler)
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/workflows", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("preflight OPTIONS request from unallowed origin returns 403 Forbidden", func(t *testing.T) {
		handler := httpmw.CORS([]string{"http://localhost:3000"}, true)(dummyHandler)
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/workflows", nil)
		req.Header.Set("Origin", "http://evil.com")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("disallowed origin on normal request does not receive CORS headers", func(t *testing.T) {
		handler := httpmw.CORS([]string{"http://localhost:3000"}, false)(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil)
		req.Header.Set("Origin", "http://untrusted.com")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
}

func TestSecurityHeaders(t *testing.T) {
	t.Run("sets all 4 security headers on 200 OK response", func(t *testing.T) {
		dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

		handler := httpmw.SecurityHeaders()(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
		assert.Equal(t, "default-src 'none'", rec.Header().Get("Content-Security-Policy"))
	})

	t.Run("sets all 4 security headers on error response", func(t *testing.T) {
		errorHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		})

		handler := httpmw.SecurityHeaders()(errorHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/error", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
		assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
		assert.Equal(t, "default-src 'none'", rec.Header().Get("Content-Security-Policy"))
	})
}
