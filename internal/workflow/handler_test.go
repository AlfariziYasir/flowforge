package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
	"flowforge/internal/platform/httpx"
	"flowforge/internal/workflow"
	workflowmocks "flowforge/internal/workflow/mocks"
)

func contextWithAuthUser(tenantID, userID uuid.UUID, role string) context.Context {
	return auth.ContextWithAuthUser(context.Background(), auth.AuthUser{
		ID:       userID,
		TenantID: tenantID,
		Email:    "user@flowforge.local",
		Role:     role,
	})
}

// T-21: Create endpoint returns 201 Created on valid input, 422 on blank name.
func TestWorkflowHandler_Create(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()

	t.Run("returns 201 Created on valid input", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)

		wf := &domain.Workflow{ID: uuid.New(), TenantID: tenantID, Name: "Test WF", Status: domain.WorkflowStatusDraft}
		ver := &domain.WorkflowVersion{ID: uuid.New(), TenantID: tenantID, VersionNumber: 1, Status: domain.VersionStatusDraft}

		mockUC.EXPECT().CreateWorkflow(mock.Anything, mock.MatchedBy(func(cmd workflow.CreateWorkflowCommand) bool {
			return cmd.Name == "Test WF" && cmd.TenantID == tenantID
		})).Return(wf, ver, nil)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.CreateWorkflowRequest{Name: "Test WF", Description: "Desc"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
		rec := httptest.NewRecorder()

		handler.Create(rec, req)

		assert.Equal(t, http.StatusCreated, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)
		assert.True(t, env.Success)
	})

	t.Run("returns 422 Unprocessable Entity when name is blank", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().CreateWorkflow(mock.Anything, mock.Anything).Return(nil, nil, workflow.ErrNameRequired)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.CreateWorkflowRequest{Name: "   "})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
		rec := httptest.NewRecorder()

		handler.Create(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)
		assert.False(t, env.Success)
		assert.Equal(t, httpx.CodeWorkflowNameRequired, env.Error.Code)
	})
}

// T-22: SaveDraft endpoint returns 200 OK on valid draft, 422 on cycle.
func TestWorkflowHandler_SaveDraft(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	t.Run("returns 200 OK on valid draft save", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)

		res := &workflow.SaveDraftResult{WorkflowID: wfID, VersionID: uuid.New(), VersionNumber: 1, Status: domain.VersionStatusDraft, RowVersion: 2, UpdatedAt: time.Now()}
		mockUC.EXPECT().SaveDraft(mock.Anything, mock.Anything).Return(res, nil)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.SaveDraftRequest{RowVersion: 1})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/"+wfID.String()+"/draft", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "editor"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.SaveDraft(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("returns 422 Unprocessable Entity when graph contains cycle", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().SaveDraft(mock.Anything, mock.Anything).Return(nil, domain.ErrCycleDetected)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.SaveDraftRequest{RowVersion: 1})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/workflows/"+wfID.String()+"/draft", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "editor"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.SaveDraft(rec, req)

		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)
		assert.Equal(t, httpx.CodeWorkflowCycleDetected, env.Error.Code)
	})
}

// T-23 / T-24: Publish endpoint returns 200 OK or 409 Conflict on stale row_version.
func TestWorkflowHandler_Publish(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	t.Run("returns 200 OK on valid publish", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)

		res := &workflow.PublishResult{
			WorkflowID:    wfID,
			VersionID:     uuid.New(),
			VersionNumber: 1,
			Status:        domain.VersionStatusPublished,
			PublishedAt:   time.Now(),
		}
		mockUC.EXPECT().PublishVersion(mock.Anything, mock.Anything).Return(res, nil)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.PublishWorkflowRequest{RowVersion: 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+wfID.String()+"/publish", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.Publish(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("returns 409 Conflict on stale row_version", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().PublishVersion(mock.Anything, mock.Anything).Return(nil, workflow.ErrVersionConflict)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.PublishWorkflowRequest{RowVersion: 1})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+wfID.String()+"/publish", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.Publish(rec, req)

		assert.Equal(t, http.StatusConflict, rec.Code)

		var env httpx.Envelope
		err := json.Unmarshal(rec.Body.Bytes(), &env)
		require.NoError(t, err)
		assert.Equal(t, httpx.CodeWorkflowVersionConflict, env.Error.Code)
	})
}

// T-25: Rollback endpoint returns 200 OK on valid rollback.
func TestWorkflowHandler_Rollback(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()
	targetVerID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)

	res := &workflow.RollbackResult{
		WorkflowID:    wfID,
		VersionID:     uuid.New(),
		VersionNumber: 4,
		Status:        domain.VersionStatusPublished,
		RolledBackAt:  time.Now(),
	}
	mockUC.EXPECT().RollbackVersion(mock.Anything, mock.Anything).Return(res, nil)

	handler := workflow.NewWorkflowHandler(mockUC)

	body, _ := json.Marshal(workflow.RollbackWorkflowRequest{RowVersion: 3})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows/"+wfID.String()+"/versions/"+targetVerID.String()+"/rollback", bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
	req.SetPathValue("workflowId", wfID.String())
	req.SetPathValue("versionId", targetVerID.String())
	rec := httptest.NewRecorder()

	handler.Rollback(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// T-39: List endpoint returns standard list envelope with pagination.
func TestWorkflowHandler_List(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)

	paginated := &workflow.PaginatedWorkflows{
		Items: []*domain.Workflow{
			{ID: uuid.New(), TenantID: tenantID, Name: "WF1"},
		},
		TotalItems: 1,
		Page:       1,
		PageSize:   20,
	}
	mockUC.EXPECT().ListWorkflows(mock.Anything, mock.Anything).Return(paginated, nil)

	handler := workflow.NewWorkflowHandler(mockUC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows?page=1&pageSize=20&status=published&search=test&sortBy=updated_at&sortOrder=asc", nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
	rec := httptest.NewRecorder()

	handler.List(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var env httpx.Envelope
	err := json.Unmarshal(rec.Body.Bytes(), &env)
	require.NoError(t, err)
	assert.True(t, env.Success)

	// Assert no tenantId leaks in workflow list items
	assert.NotContains(t, rec.Body.String(), "tenantId")
}

func TestWorkflowHandler_ListVersions_DTOShape(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)
	versions := []*domain.WorkflowVersion{
		{
			ID:            uuid.New(),
			TenantID:      tenantID,
			WorkflowID:    wfID,
			VersionNumber: 1,
			Status:        domain.VersionStatusPublished,
			GraphSnapshot: json.RawMessage(`{"nodes":[{"nodeKey":"a"}]}`),
			Metadata:      json.RawMessage(`{"author":"dev"}`),
			CreatedAt:     time.Now(),
		},
	}
	mockUC.EXPECT().ListVersions(mock.Anything, tenantID, wfID).Return(versions, nil)

	handler := workflow.NewWorkflowHandler(mockUC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+wfID.String()+"/versions", nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
	req.SetPathValue("workflowId", wfID.String())
	rec := httptest.NewRecorder()

	handler.ListVersions(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "graphSnapshot")
	assert.NotContains(t, rec.Body.String(), "metadata")
	assert.NotContains(t, rec.Body.String(), "tenantId")
}

// T-40: Get endpoint returns 200 OK or 404 Not Found.
func TestWorkflowHandler_Get(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	t.Run("returns 200 OK when found", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().GetWorkflow(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, TenantID: tenantID, Name: "WF"}, nil)

		handler := workflow.NewWorkflowHandler(mockUC)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+wfID.String(), nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.Get(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("returns 404 Not Found when missing", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().GetWorkflow(mock.Anything, tenantID, wfID).Return(nil, workflow.ErrWorkflowNotFound)

		handler := workflow.NewWorkflowHandler(mockUC)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+wfID.String(), nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.Get(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

// T-41: Archive endpoint returns 204 No Content on archive.
func TestWorkflowHandler_Archive(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	t.Run("returns 204 No Content on successful archive", func(t *testing.T) {
		mockUC := workflowmocks.NewMockWorkflowUseCase(t)
		mockUC.EXPECT().ArchiveWorkflow(mock.Anything, mock.MatchedBy(func(cmd workflow.ArchiveWorkflowCommand) bool {
			return cmd.WorkflowID == wfID && cmd.RowVersion == 1
		})).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusArchived}, nil)

		handler := workflow.NewWorkflowHandler(mockUC)

		body, _ := json.Marshal(workflow.ArchiveWorkflowRequest{RowVersion: 1})
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/workflows/"+wfID.String(), bytes.NewReader(body)).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
		req.SetPathValue("workflowId", wfID.String())
		rec := httptest.NewRecorder()

		handler.Archive(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
		assert.Equal(t, 0, rec.Body.Len())
	})
}

func TestWorkflowHandler_MaxBytesReader(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()

	handler := workflow.NewWorkflowHandler(nil)

	// Valid JSON string with printable characters exceeding 1 MiB limit
	largeJSON := `{"name":"` + string(bytes.Repeat([]byte("a"), 2<<20)) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", bytes.NewReader([]byte(largeJSON))).WithContext(contextWithAuthUser(tenantID, userID, "admin"))
	rec := httptest.NewRecorder()

	handler.Create(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// T-25: Internal server error hides raw DB/SQL syntax or pgx stack traces.
func TestWorkflowHandler_InternalServerErrorDataLeakGuard(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	wfID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)
	mockUC.EXPECT().GetWorkflow(mock.Anything, tenantID, wfID).Return(nil, errors.New("pgx: fatal SQL error table 'workflows' does not exist"))

	handler := workflow.NewWorkflowHandler(mockUC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows/"+wfID.String(), nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
	req.SetPathValue("workflowId", wfID.String())
	rec := httptest.NewRecorder()

	handler.Get(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var env httpx.Envelope
	err := json.Unmarshal(rec.Body.Bytes(), &env)
	require.NoError(t, err)
	assert.False(t, env.Success)
	assert.Equal(t, "INTERNAL_SERVER_ERROR", env.Error.Code)
	assert.Equal(t, "internal server error", env.Error.Message)
	assert.NotContains(t, rec.Body.String(), "pgx")
	assert.NotContains(t, rec.Body.String(), "table")
}

// T-40: Empty list serializes items as [] (never null).
func TestWorkflowHandler_EmptyListSerialization(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()

	mockUC := workflowmocks.NewMockWorkflowUseCase(t)
	paginated := &workflow.PaginatedWorkflows{
		Items:      []*domain.Workflow{}, // empty slice
		TotalItems: 0,
		Page:       1,
		PageSize:   20,
	}
	mockUC.EXPECT().ListWorkflows(mock.Anything, mock.Anything).Return(paginated, nil)

	handler := workflow.NewWorkflowHandler(mockUC)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflows", nil).WithContext(contextWithAuthUser(tenantID, userID, "viewer"))
	rec := httptest.NewRecorder()

	handler.List(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
	assert.NotContains(t, rec.Body.String(), `"items":null`)
}
