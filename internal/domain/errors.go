package domain

import (
	"errors"
	"fmt"
)

// Sentinel domain errors.
var (
	ErrNotFound       = errors.New("resource not found")
	ErrUnauthorized   = errors.New("unauthorized access")
	ErrForbidden      = errors.New("forbidden action")
	ErrInvalidDAG     = errors.New("invalid DAG structure")
	ErrCycleDetected  = errors.New("cycle detected in workflow graph")
	ErrTenantMismatch = errors.New("tenant ID mismatch")
	ErrConflict       = errors.New("resource state conflict")
)

// DomainError represents a structured application error with a stable error code.
type DomainError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *DomainError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *DomainError) Unwrap() error {
	return e.Err
}

// NewDomainError creates a new DomainError wrapping an optional cause error.
func NewDomainError(code, message string, cause error) *DomainError {
	return &DomainError{
		Code:    code,
		Message: message,
		Err:     cause,
	}
}
