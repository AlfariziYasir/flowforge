package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Workflow status values. These mirror the CHECK constraint on workflows.status
// in migrations/000001_init_schema.up.sql — the constraint is the contract.
const (
	WorkflowStatusDraft     = "draft"
	WorkflowStatusPublished = "published"
	WorkflowStatusArchived  = "archived"
	WorkflowStatusDisabled  = "disabled"
)

// Workflow version status values, mirroring the CHECK on workflow_versions.status.
const (
	VersionStatusDraft     = "draft"
	VersionStatusPublished = "published"
	VersionStatusArchived  = "archived"
)

// Node types, mirroring the CHECK on workflow_nodes.node_type.
const (
	NodeTypeHTTP         = "HTTP"
	NodeTypeDelay        = "DELAY"
	NodeTypeCondition    = "CONDITION"
	NodeTypeTransform    = "TRANSFORM"
	NodeTypeEventPublish = "EVENT_PUBLISH"
	NodeTypeEventWait    = "EVENT_WAIT"
)

// IsValidNodeType reports whether t is one of the supported node types.
func IsValidNodeType(t string) bool {
	switch t {
	case NodeTypeHTTP, NodeTypeDelay, NodeTypeCondition, NodeTypeTransform,
		NodeTypeEventPublish, NodeTypeEventWait:
		return true
	default:
		return false
	}
}

// Workflow is the tenant-scoped workflow metadata and current version pointer.
//
// CurrentVersionID is a pointer because the column is nullable: a freshly created
// workflow has no published version yet.
type Workflow struct {
	ID                   uuid.UUID  `db:"id" json:"id"`
	TenantID             uuid.UUID  `db:"tenant_id" json:"tenantId"`
	Name                 string     `db:"name" json:"name"`
	Description          string     `db:"description" json:"description"`
	Status               string     `db:"status" json:"status"`
	CurrentVersionNumber int        `db:"current_version_number" json:"currentVersionNumber"`
	CurrentVersionID     *uuid.UUID `db:"current_version_id" json:"currentVersionId"`
	RowVersion           int        `db:"row_version" json:"rowVersion"`
	CreatedAt            time.Time  `db:"created_at" json:"createdAt"`
	UpdatedAt            time.Time  `db:"updated_at" json:"updatedAt"`
}

// WorkflowVersion is a draft or immutable published snapshot of a workflow definition.
//
// GraphSnapshot stays empty ("{}") while Status is draft; it is populated at publish
// time from the normalized workflow_nodes / workflow_edges rows.
type WorkflowVersion struct {
	ID            uuid.UUID       `db:"id" json:"id"`
	TenantID      uuid.UUID       `db:"tenant_id" json:"tenantId"`
	WorkflowID    uuid.UUID       `db:"workflow_id" json:"workflowId"`
	VersionNumber int             `db:"version_number" json:"versionNumber"`
	Status        string          `db:"status" json:"status"`
	GraphSnapshot json.RawMessage `db:"graph_snapshot" json:"graphSnapshot"`
	Metadata      json.RawMessage `db:"metadata" json:"metadata"`
	Checksum      string          `db:"checksum" json:"checksum"`
	CreatedBy     *uuid.UUID      `db:"created_by" json:"createdBy"`
	PublishedAt   *time.Time      `db:"published_at" json:"publishedAt"`
	CreatedAt     time.Time       `db:"created_at" json:"createdAt"`
}

// WorkflowNode is the persisted form of a graph node, identified by a UUID.
// Callers outside the repository layer address nodes by NodeKey instead — see graph.go.
type WorkflowNode struct {
	ID                uuid.UUID       `db:"id" json:"id"`
	TenantID          uuid.UUID       `db:"tenant_id" json:"-"`
	WorkflowVersionID uuid.UUID       `db:"workflow_version_id" json:"-"`
	NodeKey           string          `db:"node_key" json:"nodeKey"`
	NodeType          string          `db:"node_type" json:"nodeType"`
	Config            json.RawMessage `db:"config" json:"config"`
	PositionX         int             `db:"position_x" json:"positionX"`
	PositionY         int             `db:"position_y" json:"positionY"`
	CreatedAt         time.Time       `db:"created_at" json:"-"`
}

// WorkflowEdge is the persisted form of a graph edge. Endpoints are node UUIDs,
// matching the composite foreign keys on workflow_edges.
type WorkflowEdge struct {
	ID                uuid.UUID `db:"id" json:"id"`
	TenantID          uuid.UUID `db:"tenant_id" json:"-"`
	WorkflowVersionID uuid.UUID `db:"workflow_version_id" json:"-"`
	FromNodeID        uuid.UUID `db:"from_node_id" json:"-"`
	ToNodeID          uuid.UUID `db:"to_node_id" json:"-"`
	Branch            string    `db:"branch" json:"branch"`
	CreatedAt         time.Time `db:"created_at" json:"-"`
}
