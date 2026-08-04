package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
)

// TxRunner is satisfied structurally by postgres.UnitOfWork. Declared here so the
// application layer does not depend on an infrastructure package.
type TxRunner interface {
	ExecuteInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type WorkflowUseCase interface {
	CreateWorkflow(ctx context.Context, cmd CreateWorkflowCommand) (*domain.Workflow, *domain.WorkflowVersion, error)
	GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error)
	ListWorkflows(ctx context.Context, q ListWorkflowsQuery) (*PaginatedWorkflows, error)
	UpdateWorkflow(ctx context.Context, cmd UpdateWorkflowCommand) (*domain.Workflow, error)
	ArchiveWorkflow(ctx context.Context, cmd ArchiveWorkflowCommand) (*domain.Workflow, error)

	SaveDraft(ctx context.Context, cmd SaveDraftCommand) (*SaveDraftResult, error)
	GetVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*VersionDetail, error)
	ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error)

	PublishVersion(ctx context.Context, cmd PublishCommand) (*PublishResult, error)
	RollbackVersion(ctx context.Context, cmd RollbackCommand) (*RollbackResult, error)
}

type workflowUseCase struct {
	wfRepo    WorkflowRepository
	verRepo   VersionRepository
	auditRepo AuditRepository
	txRunner  TxRunner
}

func NewWorkflowUseCase(wfRepo WorkflowRepository, verRepo VersionRepository, auditRepo AuditRepository, txRunner TxRunner) WorkflowUseCase {
	if txRunner == nil {
		panic("workflow: UnitOfWork txRunner cannot be nil")
	}
	return &workflowUseCase{
		wfRepo:    wfRepo,
		verRepo:   verRepo,
		auditRepo: auditRepo,
		txRunner:  txRunner,
	}
}

func (uc *workflowUseCase) CreateWorkflow(ctx context.Context, cmd CreateWorkflowCommand) (*domain.Workflow, *domain.WorkflowVersion, error) {
	if strings.TrimSpace(cmd.Name) == "" {
		return nil, nil, ErrNameRequired
	}

	wfID := uuid.New()
	verID := uuid.New()
	now := time.Now()

	wf := &domain.Workflow{
		ID:                   wfID,
		TenantID:             cmd.TenantID,
		Name:                 cmd.Name,
		Description:          cmd.Description,
		Status:               domain.WorkflowStatusDraft,
		CurrentVersionNumber: 0,
		CurrentVersionID:     nil,
		RowVersion:           1,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	ver := &domain.WorkflowVersion{
		ID:            verID,
		TenantID:      cmd.TenantID,
		WorkflowID:    wfID,
		VersionNumber: 1,
		Status:        domain.VersionStatusDraft,
		GraphSnapshot: json.RawMessage(`{}`),
		Metadata:      json.RawMessage(`{}`),
		CreatedBy:     &cmd.ActorID,
		CreatedAt:     now,
	}

	err := uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := uc.wfRepo.Create(txCtx, wf); err != nil {
			return err
		}
		if err := uc.verRepo.CreateVersion(txCtx, ver); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"name": wf.Name})
		return uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowCreated,
			EntityType:  "workflow",
			EntityID:    &wfID,
			Metadata:    meta,
		})
	})

	if err != nil {
		return nil, nil, err
	}

	return wf, ver, nil
}

func (uc *workflowUseCase) GetWorkflow(ctx context.Context, tenantID, workflowID uuid.UUID) (*domain.Workflow, error) {
	return uc.wfRepo.FindByID(ctx, tenantID, workflowID)
}

func (uc *workflowUseCase) ListWorkflows(ctx context.Context, q ListWorkflowsQuery) (*PaginatedWorkflows, error) {
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 20
	} else if pageSize > 100 {
		pageSize = 100
	}

	sortBy := q.SortBy
	if sortBy == "" {
		sortBy = "updated_at"
	}
	switch sortBy {
	case "name", "created_at", "updated_at", "status":
	default:
		return nil, ErrInvalidSortField
	}

	orderAsc := false
	if strings.ToLower(q.SortOrder) == "asc" {
		orderAsc = true
	}

	filter := ListWorkflowsFilter{
		TenantID:      q.TenantID,
		Page:          page,
		PageSize:      pageSize,
		Status:        q.Status,
		Search:        q.Search,
		OrderByColumn: sortBy,
		OrderAsc:      orderAsc,
		ExcludeStatus: domain.WorkflowStatusArchived,
	}
	if q.Status == domain.WorkflowStatusArchived {
		filter.ExcludeStatus = ""
	}

	items, total, err := uc.wfRepo.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	return &PaginatedWorkflows{
		Items:      items,
		TotalItems: total,
		Page:       page,
		PageSize:   pageSize,
	}, nil
}

func (uc *workflowUseCase) UpdateWorkflow(ctx context.Context, cmd UpdateWorkflowCommand) (*domain.Workflow, error) {
	wf, err := uc.wfRepo.FindByID(ctx, cmd.TenantID, cmd.WorkflowID)
	if err != nil {
		return nil, err
	}

	if wf.Status == domain.WorkflowStatusArchived {
		return nil, ErrWorkflowArchived
	}

	if cmd.Name != nil {
		if strings.TrimSpace(*cmd.Name) == "" {
			return nil, ErrNameRequired
		}
		wf.Name = *cmd.Name
	}
	if cmd.Description != nil {
		wf.Description = *cmd.Description
	}

	err = uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := uc.wfRepo.UpdateMetadata(txCtx, wf, cmd.RowVersion); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"name": wf.Name})
		return uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowUpdated,
			EntityType:  "workflow",
			EntityID:    &cmd.WorkflowID,
			Metadata:    meta,
		})
	})

	if err != nil {
		return nil, err
	}

	wf.RowVersion++
	wf.UpdatedAt = time.Now()
	return wf, nil
}

func (uc *workflowUseCase) ArchiveWorkflow(ctx context.Context, cmd ArchiveWorkflowCommand) (*domain.Workflow, error) {
	wf, err := uc.wfRepo.FindByID(ctx, cmd.TenantID, cmd.WorkflowID)
	if err != nil {
		return nil, err
	}

	if wf.Status == domain.WorkflowStatusArchived {
		return wf, nil
	}

	err = uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		if err := uc.wfRepo.UpdateStatus(txCtx, cmd.TenantID, cmd.WorkflowID, domain.WorkflowStatusArchived, cmd.RowVersion); err != nil {
			return err
		}
		return uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowArchived,
			EntityType:  "workflow",
			EntityID:    &cmd.WorkflowID,
			Metadata:    json.RawMessage(`{}`),
		})
	})

	if err != nil {
		return nil, err
	}

	wf.Status = domain.WorkflowStatusArchived
	wf.RowVersion++
	wf.UpdatedAt = time.Now()
	return wf, nil
}

func (uc *workflowUseCase) SaveDraft(ctx context.Context, cmd SaveDraftCommand) (*SaveDraftResult, error) {
	if len(cmd.Graph.Nodes) > 0 {
		if err := engine.ValidateDAG(cmd.Graph); err != nil {
			return nil, err
		}
	}

	var draftVer *domain.WorkflowVersion
	err := uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		wf, err := uc.wfRepo.FindByIDForUpdate(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		if wf.RowVersion != cmd.RowVersion {
			return ErrVersionConflict
		}

		if wf.Status == domain.WorkflowStatusArchived {
			return ErrWorkflowArchived
		}

		ver, err := uc.verRepo.FindDraftVersion(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}
		if ver.Status != domain.VersionStatusDraft {
			return ErrVersionImmutable
		}
		draftVer = ver

		now := time.Now()
		nodes, edges, err := cmd.Graph.ToPersisted(cmd.TenantID, draftVer.ID, now)
		if err != nil {
			return fmt.Errorf("%w: %v", domain.ErrInvalidDAG, err)
		}

		if err := uc.verRepo.ReplaceGraph(txCtx, cmd.TenantID, draftVer.ID, nodes, edges); err != nil {
			return err
		}

		if err := uc.wfRepo.TouchRowVersion(txCtx, cmd.TenantID, cmd.WorkflowID, cmd.RowVersion); err != nil {
			return err
		}

		meta, _ := json.Marshal(map[string]any{"versionNumber": draftVer.VersionNumber})
		return uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowDraftSaved,
			EntityType:  "workflow",
			EntityID:    &cmd.WorkflowID,
			Metadata:    meta,
		})
	})

	if err != nil {
		return nil, err
	}

	// Note: cmd.RowVersion + 1 is guaranteed to be the new stored row_version because
	// TouchRowVersion executes UPDATE workflows SET row_version = row_version + 1 WHERE row_version = cmd.RowVersion.
	bumpedRowVersion := cmd.RowVersion + 1

	return &SaveDraftResult{
		WorkflowID:    cmd.WorkflowID,
		VersionID:     draftVer.ID,
		VersionNumber: draftVer.VersionNumber,
		Status:        draftVer.Status,
		RowVersion:    bumpedRowVersion,
		UpdatedAt:     time.Now(),
	}, nil
}

func (uc *workflowUseCase) GetVersion(ctx context.Context, tenantID, workflowID, versionID uuid.UUID) (*VersionDetail, error) {
	ver, err := uc.verRepo.FindVersionByID(ctx, tenantID, workflowID, versionID)
	if err != nil {
		return nil, err
	}

	nodes, edges, err := uc.verRepo.LoadGraph(ctx, tenantID, versionID)
	if err != nil {
		return nil, err
	}

	g := domain.FromPersisted(nodes, edges)
	return &VersionDetail{
		Version: ver,
		Graph:   g,
	}, nil
}

func (uc *workflowUseCase) ListVersions(ctx context.Context, tenantID, workflowID uuid.UUID) ([]*domain.WorkflowVersion, error) {
	if _, err := uc.wfRepo.FindByID(ctx, tenantID, workflowID); err != nil {
		return nil, err
	}
	return uc.verRepo.ListVersions(ctx, tenantID, workflowID)
}

func (uc *workflowUseCase) PublishVersion(ctx context.Context, cmd PublishCommand) (*PublishResult, error) {
	var result *PublishResult

	err := uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		wf, err := uc.wfRepo.FindByIDForUpdate(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		if wf.RowVersion != cmd.RowVersion {
			return ErrVersionConflict
		}

		if wf.Status == domain.WorkflowStatusArchived {
			return ErrWorkflowArchived
		}

		draftVer, err := uc.verRepo.FindDraftVersion(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		nodes, edges, err := uc.verRepo.LoadGraph(txCtx, cmd.TenantID, draftVer.ID)
		if err != nil {
			return err
		}

		g := domain.FromPersisted(nodes, edges)
		if err := engine.ValidateDAG(g); err != nil {
			return err
		}

		snapshotBytes, err := json.Marshal(g)
		if err != nil {
			return fmt.Errorf("marshal canonical graph snapshot: %w", err)
		}
		checksum := sha256Hex(snapshotBytes)

		now := time.Now()
		if err := uc.verRepo.MarkPublished(txCtx, cmd.TenantID, draftVer.ID, checksum, snapshotBytes, now); err != nil {
			return err
		}

		if err := uc.wfRepo.SetCurrentVersion(txCtx, cmd.TenantID, cmd.WorkflowID, draftVer.ID, draftVer.VersionNumber, cmd.RowVersion); err != nil {
			return err
		}

		// Eagerly clone new draft
		newDraftID := uuid.New()
		newDraftNum := draftVer.VersionNumber + 1

		draftMeta := draftVer.Metadata
		if len(draftMeta) == 0 {
			draftMeta = json.RawMessage(`{}`)
		}

		newDraft := &domain.WorkflowVersion{
			ID:            newDraftID,
			TenantID:      cmd.TenantID,
			WorkflowID:    cmd.WorkflowID,
			VersionNumber: newDraftNum,
			Status:        domain.VersionStatusDraft,
			GraphSnapshot: json.RawMessage(`{}`),
			Metadata:      draftMeta,
			CreatedBy:     &cmd.ActorID,
			CreatedAt:     now,
		}

		if err := uc.verRepo.CreateVersion(txCtx, newDraft); err != nil {
			return err
		}

		clonedNodes, clonedEdges, err := g.ToPersisted(cmd.TenantID, newDraftID, now)
		if err != nil {
			return fmt.Errorf("clone graph for new draft: %w", err)
		}

		if err := uc.verRepo.ReplaceGraph(txCtx, cmd.TenantID, newDraftID, clonedNodes, clonedEdges); err != nil {
			return err
		}

		meta, _ := json.Marshal(map[string]any{
			"versionNumber": draftVer.VersionNumber,
			"checksum":      checksum,
		})
		if err := uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowPublished,
			EntityType:  "workflow",
			EntityID:    &cmd.WorkflowID,
			Metadata:    meta,
		}); err != nil {
			return err
		}

		result = &PublishResult{
			WorkflowID:    cmd.WorkflowID,
			VersionID:     draftVer.ID,
			VersionNumber: draftVer.VersionNumber,
			Status:        domain.VersionStatusPublished,
			PublishedAt:   now,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

func (uc *workflowUseCase) RollbackVersion(ctx context.Context, cmd RollbackCommand) (*RollbackResult, error) {
	var result *RollbackResult

	err := uc.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		wf, err := uc.wfRepo.FindByIDForUpdate(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		if wf.RowVersion != cmd.RowVersion {
			return ErrVersionConflict
		}

		if wf.Status == domain.WorkflowStatusArchived {
			return ErrWorkflowArchived
		}

		targetVer, err := uc.verRepo.FindVersionByID(txCtx, cmd.TenantID, cmd.WorkflowID, cmd.VersionID)
		if err != nil {
			return err
		}

		if targetVer.Status != domain.VersionStatusPublished {
			return ErrVersionImmutable
		}

		maxNum, err := uc.verRepo.MaxVersionNumber(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		newPublishedNum := maxNum + 1
		newPublishedID := uuid.New()
		now := time.Now()

		targetMeta := targetVer.Metadata
		if len(targetMeta) == 0 {
			targetMeta = json.RawMessage(`{}`)
		}

		newPublishedVer := &domain.WorkflowVersion{
			ID:            newPublishedID,
			TenantID:      cmd.TenantID,
			WorkflowID:    cmd.WorkflowID,
			VersionNumber: newPublishedNum,
			Status:        domain.VersionStatusPublished,
			GraphSnapshot: targetVer.GraphSnapshot,
			Metadata:      targetMeta,
			Checksum:      targetVer.Checksum,
			CreatedBy:     &cmd.ActorID,
			PublishedAt:   &now,
			CreatedAt:     now,
		}

		if err := uc.verRepo.CreateVersion(txCtx, newPublishedVer); err != nil {
			return err
		}

		targetNodes, targetEdges, err := uc.verRepo.LoadGraph(txCtx, cmd.TenantID, targetVer.ID)
		if err != nil {
			return err
		}

		g := domain.FromPersisted(targetNodes, targetEdges)
		pubNodes, pubEdges, err := g.ToPersisted(cmd.TenantID, newPublishedID, now)
		if err != nil {
			return err
		}

		if err := uc.verRepo.ReplaceGraph(txCtx, cmd.TenantID, newPublishedID, pubNodes, pubEdges); err != nil {
			return err
		}

		if err := uc.wfRepo.SetCurrentVersion(txCtx, cmd.TenantID, cmd.WorkflowID, newPublishedID, newPublishedNum, cmd.RowVersion); err != nil {
			return err
		}

		// Replace graph of existing draft (never create a 2nd draft!)
		draftVer, err := uc.verRepo.FindDraftVersion(txCtx, cmd.TenantID, cmd.WorkflowID)
		if err != nil {
			return err
		}

		draftNodes, draftEdges, err := g.ToPersisted(cmd.TenantID, draftVer.ID, now)
		if err != nil {
			return err
		}

		if err := uc.verRepo.ReplaceGraph(txCtx, cmd.TenantID, draftVer.ID, draftNodes, draftEdges); err != nil {
			return err
		}

		meta, _ := json.Marshal(map[string]any{
			"rollbackToVersion": targetVer.VersionNumber,
			"newVersionNumber":  newPublishedNum,
		})
		if err := uc.auditRepo.Record(txCtx, AuditEntry{
			TenantID:    cmd.TenantID,
			ActorUserID: &cmd.ActorID,
			Action:      ActionWorkflowRolledBack,
			EntityType:  "workflow",
			EntityID:    &cmd.WorkflowID,
			Metadata:    meta,
		}); err != nil {
			return err
		}

		result = &RollbackResult{
			WorkflowID:    cmd.WorkflowID,
			VersionID:     newPublishedID,
			VersionNumber: newPublishedNum,
			Status:        domain.VersionStatusPublished,
			RolledBackAt:  now,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
