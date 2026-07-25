package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPinger struct {
	err error
}

func (m *mockPinger) Ping(ctx context.Context) error {
	return m.err
}

func TestHealthHandler(t *testing.T) {
	tests := []struct {
		name           string
		db             Pinger
		redis          Pinger
		expectedStatus int
		expectedBody   map[string]interface{}
	}{
		{
			name:           "all dependencies healthy",
			db:             &mockPinger{err: nil},
			redis:          &mockPinger{err: nil},
			expectedStatus: http.StatusOK,
			expectedBody: map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"status":   "ok",
					"service":  "api",
					"postgres": "up",
					"redis":    "up",
				},
				"meta":  nil,
				"error": nil,
			},
		},
		{
			name:           "database unhealthy",
			db:             &mockPinger{err: errors.New("db ping timeout")},
			redis:          &mockPinger{err: nil},
			expectedStatus: http.StatusServiceUnavailable,
			expectedBody: map[string]interface{}{
				"success": false,
				"data": map[string]interface{}{
					"status":   "unhealthy",
					"service":  "api",
					"postgres": "down",
					"redis":    "up",
				},
				"meta": nil,
				"error": map[string]interface{}{
					"code":    "SERVICE_UNAVAILABLE",
					"message": "Health check failed for dependencies",
					"details": nil,
				},
			},
		},
		{
			name:           "redis unhealthy",
			db:             &mockPinger{err: nil},
			redis:          &mockPinger{err: errors.New("redis connection refused")},
			expectedStatus: http.StatusServiceUnavailable,
			expectedBody: map[string]interface{}{
				"success": false,
				"data": map[string]interface{}{
					"status":   "unhealthy",
					"service":  "api",
					"postgres": "up",
					"redis":    "down",
				},
				"meta": nil,
				"error": map[string]interface{}{
					"code":    "SERVICE_UNAVAILABLE",
					"message": "Health check failed for dependencies",
					"details": nil,
				},
			},
		},
		{
			name:           "nil pingers (not connected)",
			db:             nil,
			redis:          nil,
			expectedStatus: http.StatusServiceUnavailable,
			expectedBody: map[string]interface{}{
				"success": false,
				"data": map[string]interface{}{
					"status":   "unhealthy",
					"service":  "api",
					"postgres": "down",
					"redis":    "down",
				},
				"meta": nil,
				"error": map[string]interface{}{
					"code":    "SERVICE_UNAVAILABLE",
					"message": "Health check failed for dependencies",
					"details": nil,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := &HealthChecker{
				DB:    tt.db,
				Redis: tt.redis,
			}
			router := NewRouter(hc)

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectedStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var resp map[string]interface{}
			err := json.Unmarshal(rec.Body.Bytes(), &resp)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedBody["success"], resp["success"])

			expectedData, _ := tt.expectedBody["data"].(map[string]interface{})
			actualData, _ := resp["data"].(map[string]interface{})
			assert.Equal(t, expectedData["status"], actualData["status"])
			assert.Equal(t, expectedData["service"], actualData["service"])
			assert.Equal(t, expectedData["postgres"], actualData["postgres"])
			assert.Equal(t, expectedData["redis"], actualData["redis"])
		})
	}
}
