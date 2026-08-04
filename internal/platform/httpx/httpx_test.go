package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/platform/httpx"
)

// T-42: httpx envelope formatting, status codes, pagination.
func TestHTTPX_Envelope(t *testing.T) {
	t.Run("OK response envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.OK(rec, map[string]string{"foo": "bar"})

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)

		assert.True(t, env.Success)
		assert.Nil(t, env.Meta)
		assert.Nil(t, env.Error)
		assert.Equal(t, map[string]any{"foo": "bar"}, env.Data)
	})

	t.Run("Created response envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.Created(rec, map[string]string{"id": "123"})

		assert.Equal(t, http.StatusCreated, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)

		assert.True(t, env.Success)
		assert.Equal(t, map[string]any{"id": "123"}, env.Data)
	})

	t.Run("NoContent response envelope writes zero bytes", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.NoContent(rec)

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, rec.Body.Bytes())
	})

	t.Run("Fail response envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		httpx.Fail(rec, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid payload")

		assert.Equal(t, http.StatusBadRequest, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)

		assert.False(t, env.Success)
		assert.Nil(t, env.Data)
		require.NotNil(t, env.Error)
		assert.Equal(t, httpx.CodeInvalidRequestBody, env.Error.Code)
		assert.Equal(t, "invalid payload", env.Error.Message)
		assert.Nil(t, env.Error.Details)
	})

	t.Run("FailWithDetails response envelope", func(t *testing.T) {
		rec := httptest.NewRecorder()
		details := map[string]string{"field": "name"}
		httpx.FailWithDetails(rec, http.StatusUnprocessableEntity, httpx.CodeValidationError, "validation failed", details)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)

		assert.False(t, env.Success)
		require.NotNil(t, env.Error)
		assert.Equal(t, map[string]any{"field": "name"}, env.Error.Details)
	})
}

func TestHTTPX_Pagination(t *testing.T) {
	t.Run("calculates totalPages and navigation flags", func(t *testing.T) {
		p := httpx.NewPagination(2, 20, 45)
		assert.Equal(t, 2, p.Page)
		assert.Equal(t, 20, p.PageSize)
		assert.Equal(t, int64(45), p.TotalItems)
		assert.Equal(t, 3, p.TotalPages)
		assert.True(t, p.HasNext)
		assert.True(t, p.HasPrev)
	})

	t.Run("handles zero items", func(t *testing.T) {
		p := httpx.NewPagination(1, 20, 0)
		assert.Equal(t, 1, p.Page)
		assert.Equal(t, 20, p.PageSize)
		assert.Equal(t, int64(0), p.TotalItems)
		assert.Equal(t, 0, p.TotalPages)
		assert.False(t, p.HasNext)
		assert.False(t, p.HasPrev)
	})

	t.Run("handles first page", func(t *testing.T) {
		p := httpx.NewPagination(1, 20, 45)
		assert.False(t, p.HasPrev)
		assert.True(t, p.HasNext)
	})

	t.Run("handles last page", func(t *testing.T) {
		p := httpx.NewPagination(3, 20, 45)
		assert.True(t, p.HasPrev)
		assert.False(t, p.HasNext)
	})
}
