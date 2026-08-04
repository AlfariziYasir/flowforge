package workflow

import (
	"time"

	"github.com/google/uuid"

	"flowforge/internal/domain"
)

// ---------- commands / queries (use-case inputs) ----------

type CreateWorkflowCommand struct {
	TenantID    uuid.UUID
	ActorID     uuid.UUID
	Name        string
	Description string
}

type UpdateWorkflowCommand struct {
	TenantID    uuid.UUID
	ActorID     uuid.UUID
	WorkflowID  uuid.UUID
	Name        *string
	Description *string
	RowVersion  int
}

type ArchiveWorkflowCommand struct {
	TenantID   uuid.UUID
	ActorID    uuid.UUID
	WorkflowID uuid.UUID
	RowVersion int
}

type ListWorkflowsQuery struct {
	TenantID  uuid.UUID
	Page      int
	PageSize  int
	Status    string
	Search    string
	SortBy    string
	SortOrder string
}

type SaveDraftCommand struct {
	TenantID   uuid.UUID
	ActorID    uuid.UUID
	WorkflowID uuid.UUID
	Graph      domain.Graph
	RowVersion int
}

type PublishCommand struct {
	TenantID   uuid.UUID
	ActorID    uuid.UUID
	WorkflowID uuid.UUID
	RowVersion int
}

type RollbackCommand struct {
	TenantID   uuid.UUID
	ActorID    uuid.UUID
	WorkflowID uuid.UUID
	VersionID  uuid.UUID
	RowVersion int
}

// ---------- results (use-case outputs) ----------

type PaginatedWorkflows struct {
	Items      []*domain.Workflow
	TotalItems int64
	Page       int
	PageSize   int
}

type VersionDetail struct {
	Version *domain.WorkflowVersion
	Graph   domain.Graph
}

type PublishResult struct {
	WorkflowID    uuid.UUID `json:"workflowId"`
	VersionID     uuid.UUID `json:"versionId"`
	VersionNumber int       `json:"versionNumber"`
	Status        string    `json:"status"`
	PublishedAt   time.Time `json:"publishedAt"`
}

type RollbackResult struct {
	WorkflowID    uuid.UUID `json:"workflowId"`
	VersionID     uuid.UUID `json:"versionId"`
	VersionNumber int       `json:"versionNumber"`
	Status        string    `json:"status"`
	RolledBackAt  time.Time `json:"rolledBackAt"`
}

type SaveDraftResult struct {
	WorkflowID    uuid.UUID `json:"workflowId"`
	VersionID     uuid.UUID `json:"versionId"`
	VersionNumber int       `json:"versionNumber"`
	Status        string    `json:"status"`
	RowVersion    int       `json:"rowVersion"` // bumped value — clients chain on this
	UpdatedAt     time.Time `json:"updatedAt"`
}

type WorkflowSummary struct {
	ID                   uuid.UUID  `json:"id"`
	Name                 string     `json:"name"`
	Description          string     `json:"description"`
	Status               string     `json:"status"`
	CurrentVersionNumber int        `json:"currentVersionNumber"`
	CurrentVersionID     *uuid.UUID `json:"currentVersionId,omitempty"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type WorkflowDetail struct {
	ID                   uuid.UUID  `json:"id"`
	Name                 string     `json:"name"`
	Description          string     `json:"description"`
	Status               string     `json:"status"`
	CurrentVersionNumber int        `json:"currentVersionNumber"`
	CurrentVersionID     *uuid.UUID `json:"currentVersionId,omitempty"`
	RowVersion           int        `json:"rowVersion"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

type WorkflowUpdatedResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	RowVersion  int       `json:"rowVersion"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type VersionSummary struct {
	ID            uuid.UUID  `json:"id"`
	WorkflowID    uuid.UUID  `json:"workflowId"`
	VersionNumber int        `json:"versionNumber"`
	Status        string     `json:"status"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

func NewWorkflowSummary(wf *domain.Workflow) WorkflowSummary {
	if wf == nil {
		return WorkflowSummary{}
	}
	return WorkflowSummary{
		ID:                   wf.ID,
		Name:                 wf.Name,
		Description:          wf.Description,
		Status:               wf.Status,
		CurrentVersionNumber: wf.CurrentVersionNumber,
		CurrentVersionID:     wf.CurrentVersionID,
		UpdatedAt:            wf.UpdatedAt,
	}
}

func NewWorkflowDetail(wf *domain.Workflow) WorkflowDetail {
	if wf == nil {
		return WorkflowDetail{}
	}
	return WorkflowDetail{
		ID:                   wf.ID,
		Name:                 wf.Name,
		Description:          wf.Description,
		Status:               wf.Status,
		CurrentVersionNumber: wf.CurrentVersionNumber,
		CurrentVersionID:     wf.CurrentVersionID,
		RowVersion:           wf.RowVersion,
		CreatedAt:            wf.CreatedAt,
		UpdatedAt:            wf.UpdatedAt,
	}
}

func NewWorkflowUpdatedResponse(wf *domain.Workflow) WorkflowUpdatedResponse {
	if wf == nil {
		return WorkflowUpdatedResponse{}
	}
	return WorkflowUpdatedResponse{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		RowVersion:  wf.RowVersion,
		UpdatedAt:   wf.UpdatedAt,
	}
}

func NewVersionSummary(v *domain.WorkflowVersion) VersionSummary {
	if v == nil {
		return VersionSummary{}
	}
	return VersionSummary{
		ID:            v.ID,
		WorkflowID:    v.WorkflowID,
		VersionNumber: v.VersionNumber,
		Status:        v.Status,
		PublishedAt:   v.PublishedAt,
		CreatedAt:     v.CreatedAt,
	}
}
