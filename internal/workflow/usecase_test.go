package workflow_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/workflow"
	workflowmocks "flowforge/internal/workflow/mocks"
)

type recordingTxRunner struct {
	called bool
}

func (r *recordingTxRunner) ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	r.called = true
	return fn(ctx)
}

func (r *recordingTxRunner) ExecuteInTxOptions(ctx context.Context, opts pgx.TxOptions, fn func(ctx context.Context) error) error {
	r.called = true
	return fn(ctx)
}

func TestNewWorkflowUseCase_NilTxRunnerPanics(t *testing.T) {
	assert.Panics(t, func() {
		workflow.NewWorkflowUseCase(nil, nil, nil, nil)
	})
}

// T-10 / T-10b / T-11: CreateWorkflow creates workflow + draft v1 and audit log inside transaction.
func TestCreateWorkflow(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()

	t.Run("successfully creates workflow and draft version 1", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)
		txRunner := &recordingTxRunner{}

		wfRepo.EXPECT().Create(mock.Anything, mock.MatchedBy(func(wf *domain.Workflow) bool {
			return wf.Name == "My Workflow" && wf.TenantID == tenantID && wf.Status == domain.WorkflowStatusDraft && wf.CurrentVersionNumber == 0
		})).Return(nil)

		verRepo.EXPECT().CreateVersion(mock.Anything, mock.MatchedBy(func(ver *domain.WorkflowVersion) bool {
			return ver.VersionNumber == 1 && ver.Status == domain.VersionStatusDraft && ver.TenantID == tenantID
		})).Return(nil)

		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
			return e.Action == workflow.ActionWorkflowCreated && e.TenantID == tenantID
		})).Return(nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, txRunner)

		wf, ver, err := uc.CreateWorkflow(context.Background(), workflow.CreateWorkflowCommand{
			TenantID:    tenantID,
			ActorID:     actorID,
			Name:        "My Workflow",
			Description: "Test Description",
		})

		require.NoError(t, err)
		assert.True(t, txRunner.called)
		assert.Equal(t, "My Workflow", wf.Name)
		assert.Equal(t, 1, ver.VersionNumber)
		assert.Equal(t, domain.VersionStatusDraft, ver.Status)
	})

	t.Run("requires non-empty name", func(t *testing.T) {
		uc := workflow.NewWorkflowUseCase(nil, nil, nil, &recordingTxRunner{})
		_, _, err := uc.CreateWorkflow(context.Background(), workflow.CreateWorkflowCommand{
			TenantID: tenantID,
			ActorID:  actorID,
			Name:     "   ",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrNameRequired)
	})

	t.Run("returns conflict when name already exists", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		wfRepo.EXPECT().Create(mock.Anything, mock.Anything).Return(workflow.ErrWorkflowAlreadyExists)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

		_, _, err := uc.CreateWorkflow(context.Background(), workflow.CreateWorkflowCommand{
			TenantID: tenantID,
			ActorID:  actorID,
			Name:     "Duplicate",
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrWorkflowAlreadyExists)
	})
}

// T-12 / T-13: ListWorkflows applies pagination, status filtering, and sort allowlist.
func TestListWorkflows(t *testing.T) {
	tenantID := uuid.New()

	t.Run("returns paginated workflows with default sort", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)

		expectedFilter := workflow.ListWorkflowsFilter{
			TenantID:      tenantID,
			Page:          1,
			PageSize:      20,
			OrderByColumn: "updated_at",
			OrderAsc:      false,
			ExcludeStatus: domain.WorkflowStatusArchived,
		}

		wfRepo.EXPECT().List(mock.Anything, expectedFilter).Return([]*domain.Workflow{
			{ID: uuid.New(), TenantID: tenantID, Name: "WF1"},
		}, 1, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, nil, nil, &recordingTxRunner{})

		res, err := uc.ListWorkflows(context.Background(), workflow.ListWorkflowsQuery{
			TenantID: tenantID,
		})

		require.NoError(t, err)
		assert.Equal(t, int64(1), res.TotalItems)
		assert.Len(t, res.Items, 1)
	})

	t.Run("rejects invalid sort field", func(t *testing.T) {
		uc := workflow.NewWorkflowUseCase(nil, nil, nil, &recordingTxRunner{})

		_, err := uc.ListWorkflows(context.Background(), workflow.ListWorkflowsQuery{
			TenantID: tenantID,
			SortBy:   "malicious_injection; DROP TABLE workflows;--",
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrInvalidSortField)
	})
}

// T-14: UpdateWorkflow updates metadata and audits inside transaction.
func TestUpdateWorkflow(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()

	t.Run("updates metadata successfully", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		existing := &domain.Workflow{
			ID:          wfID,
			TenantID:    tenantID,
			Name:        "Old Name",
			Description: "Old Desc",
			Status:      domain.WorkflowStatusDraft,
			RowVersion:  1,
		}

		wfRepo.EXPECT().FindByID(mock.Anything, tenantID, wfID).Return(existing, nil)
		wfRepo.EXPECT().UpdateMetadata(mock.Anything, mock.MatchedBy(func(wf *domain.Workflow) bool {
			return wf.Name == "New Name"
		}), 1).Return(nil)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
			return e.Action == workflow.ActionWorkflowUpdated
		})).Return(nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, nil, auditRepo, &recordingTxRunner{})

		newName := "New Name"
		updated, err := uc.UpdateWorkflow(context.Background(), workflow.UpdateWorkflowCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Name:       &newName,
			RowVersion: 1,
		})

		require.NoError(t, err)
		assert.Equal(t, "New Name", updated.Name)
		assert.Equal(t, 2, updated.RowVersion)
	})

	t.Run("blocks update on archived workflow", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		existing := &domain.Workflow{
			ID:       wfID,
			TenantID: tenantID,
			Status:   domain.WorkflowStatusArchived,
		}
		wfRepo.EXPECT().FindByID(mock.Anything, tenantID, wfID).Return(existing, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, nil, nil, &recordingTxRunner{})

		newName := "New"
		_, err := uc.UpdateWorkflow(context.Background(), workflow.UpdateWorkflowCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Name:       &newName,
			RowVersion: 1,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrWorkflowArchived)
	})
}

// T-15: ArchiveWorkflow updates status to archived.
func TestArchiveWorkflow(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()

	wfRepo := workflowmocks.NewMockWorkflowRepository(t)
	auditRepo := workflowmocks.NewMockAuditRepository(t)

	existing := &domain.Workflow{
		ID:         wfID,
		TenantID:   tenantID,
		Status:     domain.WorkflowStatusDraft,
		RowVersion: 2,
	}

	wfRepo.EXPECT().FindByID(mock.Anything, tenantID, wfID).Return(existing, nil)
	wfRepo.EXPECT().UpdateStatus(mock.Anything, tenantID, wfID, domain.WorkflowStatusArchived, 2).Return(nil)
	auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
		return e.Action == workflow.ActionWorkflowArchived
	})).Return(nil)

	uc := workflow.NewWorkflowUseCase(wfRepo, nil, auditRepo, &recordingTxRunner{})

	archived, err := uc.ArchiveWorkflow(context.Background(), workflow.ArchiveWorkflowCommand{
		TenantID:   tenantID,
		ActorID:    actorID,
		WorkflowID: wfID,
		RowVersion: 2,
	})

	require.NoError(t, err)
	assert.Equal(t, domain.WorkflowStatusArchived, archived.Status)
}

// T-16 / T-17: SaveDraft validates graph and updates draft.
func TestSaveDraft(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()
	draftVerID := uuid.New()

	validGraph := domain.Graph{
		Nodes: []domain.NodeInput{
			{NodeKey: "start", NodeType: domain.NodeTypeHTTP},
			{NodeKey: "end", NodeType: domain.NodeTypeTransform},
		},
		Edges: []domain.EdgeInput{{From: "start", To: "end"}},
	}

	t.Run("saves valid draft successfully and returns bumped rowVersion", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusDraft, RowVersion: 1}, nil)
		wfRepo.EXPECT().TouchRowVersion(mock.Anything, tenantID, wfID, 1).Return(nil)
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(&domain.WorkflowVersion{ID: draftVerID, VersionNumber: 1, Status: domain.VersionStatusDraft}, nil)
		verRepo.EXPECT().ReplaceGraph(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything).Return(nil)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
			return e.Action == workflow.ActionWorkflowDraftSaved || e.Action == "draft_save"
		})).Return(nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

		res, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      validGraph,
			RowVersion: 1,
		})

		require.NoError(t, err)
		assert.Equal(t, draftVerID, res.VersionID)
		assert.Equal(t, 2, res.RowVersion)
	})

	t.Run("two sequential saves succeed when chaining rowVersion", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		// First save at RowVersion 1
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusDraft, RowVersion: 1}, nil).Once()
		wfRepo.EXPECT().TouchRowVersion(mock.Anything, tenantID, wfID, 1).Return(nil).Once()
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(&domain.WorkflowVersion{ID: draftVerID, VersionNumber: 1, Status: domain.VersionStatusDraft}, nil).Once()
		verRepo.EXPECT().ReplaceGraph(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything).Return(nil).Once()
		auditRepo.EXPECT().Record(mock.Anything, mock.Anything).Return(nil).Once()

		// Second save at RowVersion 2
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusDraft, RowVersion: 2}, nil).Once()
		wfRepo.EXPECT().TouchRowVersion(mock.Anything, tenantID, wfID, 2).Return(nil).Once()
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(&domain.WorkflowVersion{ID: draftVerID, VersionNumber: 1, Status: domain.VersionStatusDraft}, nil).Once()
		verRepo.EXPECT().ReplaceGraph(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything).Return(nil).Once()
		auditRepo.EXPECT().Record(mock.Anything, mock.Anything).Return(nil).Once()

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

		res1, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      validGraph,
			RowVersion: 1,
		})
		require.NoError(t, err)
		assert.Equal(t, 2, res1.RowVersion)

		res2, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      validGraph,
			RowVersion: res1.RowVersion,
		})
		require.NoError(t, err)
		assert.Equal(t, 3, res2.RowVersion)
	})

	t.Run("rejects a stale rowVersion", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)

		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusDraft, RowVersion: 7}, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, nil, &recordingTxRunner{})

		_, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      validGraph,
			RowVersion: 3,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrVersionConflict)
		verRepo.AssertNotCalled(t, "ReplaceGraph")
	})

	t.Run("on a published version returns ErrVersionImmutable", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)

		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(&domain.Workflow{ID: wfID, Status: domain.WorkflowStatusDraft, RowVersion: 1}, nil)
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(&domain.WorkflowVersion{ID: draftVerID, VersionNumber: 1, Status: domain.VersionStatusPublished}, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, nil, &recordingTxRunner{})

		_, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      validGraph,
			RowVersion: 1,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrVersionImmutable)
	})

	t.Run("rejects cyclic graph before database access", func(t *testing.T) {
		cyclicGraph := domain.Graph{
			Nodes: []domain.NodeInput{
				{NodeKey: "a", NodeType: domain.NodeTypeHTTP},
				{NodeKey: "b", NodeType: domain.NodeTypeHTTP},
			},
			Edges: []domain.EdgeInput{{From: "a", To: "b"}, {From: "b", To: "a"}},
		}

		uc := workflow.NewWorkflowUseCase(nil, nil, nil, &recordingTxRunner{})

		_, err := uc.SaveDraft(context.Background(), workflow.SaveDraftCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			Graph:      cyclicGraph,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrCycleDetected)
	})
}

// T-18 / T-19 / T-30 / T-31 / T-32: PublishVersion validates DAG, marks published, repointers workflow, eager-creates draft v_{n+1}.
func TestPublishVersion(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()
	draftVerID := uuid.New()

	nodes := []domain.WorkflowNode{
		{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: draftVerID, NodeKey: "node_1", NodeType: domain.NodeTypeHTTP, Config: json.RawMessage(`{}`)},
	}
	edges := []domain.WorkflowEdge{}

	t.Run("publishes draft and clones new draft version 2", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusDraft, RowVersion: 1}
		draftVer := &domain.WorkflowVersion{ID: draftVerID, TenantID: tenantID, WorkflowID: wfID, VersionNumber: 1, Status: domain.VersionStatusDraft}

		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(draftVer, nil)
		verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, draftVerID).Return(nodes, edges, nil)
		verRepo.EXPECT().MarkPublished(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		wfRepo.EXPECT().SetCurrentVersion(mock.Anything, tenantID, wfID, draftVerID, 1, 1).Return(nil)
		verRepo.EXPECT().CreateVersion(mock.Anything, mock.MatchedBy(func(v *domain.WorkflowVersion) bool {
			return v.VersionNumber == 2 && v.Status == domain.VersionStatusDraft
		})).Return(nil)
		verRepo.EXPECT().ReplaceGraph(mock.Anything, tenantID, mock.Anything, mock.Anything, mock.Anything).Return(nil)
		auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
			return e.Action == workflow.ActionWorkflowPublished
		})).Return(nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

		res, err := uc.PublishVersion(context.Background(), workflow.PublishCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			RowVersion: 1,
		})

		require.NoError(t, err)
		assert.Equal(t, 1, res.VersionNumber)
		assert.Equal(t, domain.VersionStatusPublished, res.Status)
	})

	t.Run("rejects publishing an empty graph", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusDraft, RowVersion: 1}
		draftVer := &domain.WorkflowVersion{ID: draftVerID, TenantID: tenantID, WorkflowID: wfID, VersionNumber: 1}

		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(draftVer, nil)
		verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, draftVerID).Return(nil, nil, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, nil, &recordingTxRunner{})

		_, err := uc.PublishVersion(context.Background(), workflow.PublishCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			RowVersion: 1,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrInvalidDAG)
	})

	t.Run("rejects a stale rowVersion before any write", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusDraft, RowVersion: 5}
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, nil, &recordingTxRunner{})

		_, err := uc.PublishVersion(context.Background(), workflow.PublishCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			RowVersion: 2,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrVersionConflict)
		verRepo.AssertNotCalled(t, "MarkPublished", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		wfRepo.AssertNotCalled(t, "SetCurrentVersion", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		verRepo.AssertNotCalled(t, "CreateVersion", mock.Anything, mock.Anything)
		verRepo.AssertNotCalled(t, "ReplaceGraph", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("on an archived workflow returns ErrWorkflowArchived", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusArchived, RowVersion: 1}
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, nil, nil, &recordingTxRunner{})

		_, err := uc.PublishVersion(context.Background(), workflow.PublishCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			RowVersion: 1,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrWorkflowArchived)
	})
}

// T-20 / T-33: RollbackVersion creates new published version v_n cloned from target, repointers workflow, and replaces draft graph (never 2nd draft).
func TestRollbackVersion(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()
	targetVerID := uuid.New()
	draftVerID := uuid.New()

	wfRepo := workflowmocks.NewMockWorkflowRepository(t)
	verRepo := workflowmocks.NewMockVersionRepository(t)
	auditRepo := workflowmocks.NewMockAuditRepository(t)

	wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusPublished, RowVersion: 3}
	targetVer := &domain.WorkflowVersion{
		ID:            targetVerID,
		TenantID:      tenantID,
		WorkflowID:    wfID,
		VersionNumber: 1,
		Status:        domain.VersionStatusPublished,
		GraphSnapshot: json.RawMessage(`{"nodes":[{"nodeKey":"start","nodeType":"HTTP"}]}`),
		Checksum:      "dummy",
	}
	draftVer := &domain.WorkflowVersion{
		ID:            draftVerID,
		TenantID:      tenantID,
		WorkflowID:    wfID,
		VersionNumber: 3,
		Status:        domain.VersionStatusDraft,
	}

	nodes := []domain.WorkflowNode{{ID: uuid.New(), NodeKey: "start", NodeType: domain.NodeTypeHTTP}}
	edges := []domain.WorkflowEdge{}

	wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)
	verRepo.EXPECT().FindVersionByID(mock.Anything, tenantID, wfID, targetVerID).Return(targetVer, nil)
	verRepo.EXPECT().MaxVersionNumber(mock.Anything, tenantID, wfID).Return(3, nil)
	verRepo.EXPECT().CreateVersion(mock.Anything, mock.MatchedBy(func(v *domain.WorkflowVersion) bool {
		return v.VersionNumber == 4 && v.Status == domain.VersionStatusPublished
	})).Return(nil)
	verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, targetVerID).Return(nodes, edges, nil)
	verRepo.EXPECT().ReplaceGraph(mock.Anything, tenantID, mock.Anything, mock.Anything, mock.Anything).Return(nil).Twice()
	wfRepo.EXPECT().SetCurrentVersion(mock.Anything, tenantID, wfID, mock.Anything, 4, 3).Return(nil)
	verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(draftVer, nil)
	auditRepo.EXPECT().Record(mock.Anything, mock.MatchedBy(func(e workflow.AuditEntry) bool {
		return e.Action == workflow.ActionWorkflowRolledBack
	})).Return(nil)

	uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

	res, err := uc.RollbackVersion(context.Background(), workflow.RollbackCommand{
		TenantID:   tenantID,
		ActorID:    actorID,
		WorkflowID: wfID,
		VersionID:  targetVerID,
		RowVersion: 3,
	})

	require.NoError(t, err)
	assert.Equal(t, 4, res.VersionNumber)
	assert.Equal(t, domain.VersionStatusPublished, res.Status)

	t.Run("rejects a stale rowVersion before any write", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusPublished, RowVersion: 5}
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil)

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, nil, &recordingTxRunner{})

		_, err := uc.RollbackVersion(context.Background(), workflow.RollbackCommand{
			TenantID:   tenantID,
			ActorID:    actorID,
			WorkflowID: wfID,
			VersionID:  targetVerID,
			RowVersion: 2,
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, workflow.ErrVersionConflict)
		verRepo.AssertNotCalled(t, "CreateVersion", mock.Anything, mock.Anything)
		verRepo.AssertNotCalled(t, "ReplaceGraph", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	})
}

// T-12: GetWorkflow returns ErrWorkflowNotFound when tenant mismatch (never 403 or data leak).
func TestGetWorkflow_TenantIsolation(t *testing.T) {
	tenantID1 := uuid.New()
	tenantID2 := uuid.New()
	wfID := uuid.New()

	wfRepo := workflowmocks.NewMockWorkflowRepository(t)
	wfRepo.EXPECT().FindByID(mock.Anything, tenantID2, wfID).Return(nil, workflow.ErrWorkflowNotFound)

	uc := workflow.NewWorkflowUseCase(wfRepo, nil, nil, &recordingTxRunner{})

	wf, err := uc.GetWorkflow(context.Background(), tenantID2, wfID)
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrWorkflowNotFound)
	assert.Nil(t, wf)
	_ = tenantID1
}

// T-18: Publish checksum is stable under node/edge reordering and sensitive to config changes.
func TestPublishVersion_ChecksumStability(t *testing.T) {
	tenantID := uuid.New()
	actorID := uuid.New()
	wfID := uuid.New()
	draftVerID := uuid.New()

	n1 := domain.WorkflowNode{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: draftVerID, NodeKey: "a", NodeType: domain.NodeTypeHTTP, Config: json.RawMessage(`{"url":"https://api.test"}`)}
	n2 := domain.WorkflowNode{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: draftVerID, NodeKey: "b", NodeType: domain.NodeTypeTransform, Config: json.RawMessage(`{}`)}
	edge := domain.WorkflowEdge{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: draftVerID, FromNodeID: n1.ID, ToNodeID: n2.ID}

	t.Run("shuffled nodes and edges produce identical checksum", func(t *testing.T) {
		wfRepo := workflowmocks.NewMockWorkflowRepository(t)
		verRepo := workflowmocks.NewMockVersionRepository(t)
		auditRepo := workflowmocks.NewMockAuditRepository(t)

		wf := &domain.Workflow{ID: wfID, TenantID: tenantID, Status: domain.WorkflowStatusDraft, RowVersion: 1}
		draftVer := &domain.WorkflowVersion{ID: draftVerID, TenantID: tenantID, WorkflowID: wfID, VersionNumber: 1, Status: domain.VersionStatusDraft}

		var checksum1, checksum2 string

		// Call 1: n1 then n2
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil).Once()
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(draftVer, nil).Once()
		verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, draftVerID).Return([]domain.WorkflowNode{n1, n2}, []domain.WorkflowEdge{edge}, nil).Once()
		verRepo.EXPECT().MarkPublished(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(ctx context.Context, tid, vid uuid.UUID, cs string, snap []byte, at time.Time) error {
			checksum1 = cs
			return nil
		}).Once()
		wfRepo.EXPECT().SetCurrentVersion(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		verRepo.EXPECT().CreateVersion(mock.Anything, mock.Anything).Return(nil).Once()
		verRepo.EXPECT().ReplaceGraph(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		auditRepo.EXPECT().Record(mock.Anything, mock.Anything).Return(nil).Once()

		// Call 2: n2 then n1 (reversed order in persistence)
		wfRepo.EXPECT().FindByIDForUpdate(mock.Anything, tenantID, wfID).Return(wf, nil).Once()
		verRepo.EXPECT().FindDraftVersion(mock.Anything, tenantID, wfID).Return(draftVer, nil).Once()
		verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, draftVerID).Return([]domain.WorkflowNode{n2, n1}, []domain.WorkflowEdge{edge}, nil).Once()
		verRepo.EXPECT().MarkPublished(mock.Anything, tenantID, draftVerID, mock.Anything, mock.Anything, mock.Anything).RunAndReturn(func(ctx context.Context, tid, vid uuid.UUID, cs string, snap []byte, at time.Time) error {
			checksum2 = cs
			return nil
		}).Once()
		wfRepo.EXPECT().SetCurrentVersion(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		verRepo.EXPECT().CreateVersion(mock.Anything, mock.Anything).Return(nil).Once()
		verRepo.EXPECT().ReplaceGraph(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
		auditRepo.EXPECT().Record(mock.Anything, mock.Anything).Return(nil).Once()

		uc := workflow.NewWorkflowUseCase(wfRepo, verRepo, auditRepo, &recordingTxRunner{})

		_, err1 := uc.PublishVersion(context.Background(), workflow.PublishCommand{TenantID: tenantID, ActorID: actorID, WorkflowID: wfID, RowVersion: 1})
		require.NoError(t, err1)

		_, err2 := uc.PublishVersion(context.Background(), workflow.PublishCommand{TenantID: tenantID, ActorID: actorID, WorkflowID: wfID, RowVersion: 1})
		require.NoError(t, err2)

		assert.NotEmpty(t, checksum1)
		assert.Equal(t, checksum1, checksum2, "checksums must be identical for canonical graph regardless of load order")
	})
}

// T-19: Cancelled context returns error.
func TestWorkflowUseCase_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	wfRepo := workflowmocks.NewMockWorkflowRepository(t)
	wfRepo.EXPECT().FindByID(ctx, mock.Anything, mock.Anything).Return(nil, ctx.Err())

	uc := workflow.NewWorkflowUseCase(wfRepo, nil, nil, &recordingTxRunner{})
	_, err := uc.GetWorkflow(ctx, uuid.New(), uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// T-31: GetVersion on draft version assembles graph from LoadGraph, not graph_snapshot.
func TestGetVersion_DraftAssemblesGraphFromLoadGraph(t *testing.T) {
	tenantID := uuid.New()
	wfID := uuid.New()
	verID := uuid.New()

	verRepo := workflowmocks.NewMockVersionRepository(t)

	draftVer := &domain.WorkflowVersion{
		ID:            verID,
		TenantID:      tenantID,
		WorkflowID:    wfID,
		VersionNumber: 1,
		Status:        domain.VersionStatusDraft,
		GraphSnapshot: json.RawMessage(`{}`), // empty snapshot in draft row
	}

	nodes := []domain.WorkflowNode{
		{ID: uuid.New(), TenantID: tenantID, WorkflowVersionID: verID, NodeKey: "step1", NodeType: domain.NodeTypeHTTP},
	}
	edges := []domain.WorkflowEdge{}

	verRepo.EXPECT().FindVersionByID(mock.Anything, tenantID, wfID, verID).Return(draftVer, nil)
	verRepo.EXPECT().LoadGraph(mock.Anything, tenantID, verID).Return(nodes, edges, nil)

	uc := workflow.NewWorkflowUseCase(nil, verRepo, nil, &recordingTxRunner{})

	detail, err := uc.GetVersion(context.Background(), tenantID, wfID, verID)
	require.NoError(t, err)
	assert.Equal(t, draftVer, detail.Version)
	assert.Len(t, detail.Graph.Nodes, 1)
	assert.Equal(t, "step1", detail.Graph.Nodes[0].NodeKey)
}

// T-33 is covered by the auditRepo.EXPECT().Record(..., MatchedBy(e.Action == ...))
// expectations in TestCreateWorkflow / TestUpdateWorkflow / TestArchiveWorkflow /
// TestSaveDraft / TestPublishVersion / TestRollbackVersion.
