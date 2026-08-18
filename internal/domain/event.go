package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event represents a real-time platform event published over Redis Pub/Sub
// and streamed to clients via Server-Sent Events (SSE).
type Event struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	TenantID  uuid.UUID       `json:"tenantId"`
	RunID     *uuid.UUID      `json:"runId,omitempty"`
	StepID    *uuid.UUID      `json:"stepId,omitempty"`
	Status    string          `json:"status,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      map[string]any  `json:"data,omitempty"`
	Extra     json.RawMessage `json:"extra,omitempty"`
}

// SSE Event types per api-4.md §11.6
const (
	EventRunCreated         = "workflow.run.created"
	EventRunQueued          = "workflow.run.queued"
	EventRunStarted         = "workflow.run.started"
	EventRunCompleted       = "workflow.run.completed"
	EventRunFailed          = "workflow.run.failed"
	EventRunCancelRequested = "workflow.run.cancelRequested"
	EventRunCancelled       = "workflow.run.cancelled"
	EventRunRetryRequested  = "workflow.run.retryRequested"
	EventRunWaiting         = "workflow.run.waiting"
	EventStepStarted        = "step.started"
	EventStepCompleted      = "step.completed"
	EventStepFailed         = "step.failed"
	EventStepRetrying       = "step.retrying"
	EventStepWaiting        = "step.waiting"
	EventAnalysisCompleted  = "workflow.analysis.completed"
	EventHeartbeat          = "heartbeat"
)
