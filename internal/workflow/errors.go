package workflow

import "errors"

var (
	ErrWorkflowNotFound      = errors.New("workflow not found")
	ErrWorkflowAlreadyExists = errors.New("workflow name already exists in tenant")
	ErrWorkflowArchived      = errors.New("workflow is archived")
	ErrVersionNotFound       = errors.New("workflow version not found")
	ErrVersionConflict       = errors.New("stale row version")
	ErrVersionImmutable      = errors.New("published version is immutable")
	ErrDraftMissing          = errors.New("workflow has no draft version")
	ErrNameRequired          = errors.New("workflow name is required")
	ErrInvalidSortField      = errors.New("invalid sort field")
)
