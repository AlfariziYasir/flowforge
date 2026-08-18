package httpx

import (
	"encoding/json"
	"math"
	"net/http"
)

// Standard error codes per api-1.md §7.2
const (
	CodeAuthUnauthorized         = "AUTH_UNAUTHORIZED"
	CodeAuthForbidden            = "AUTH_FORBIDDEN"
	CodeWorkflowNotFound         = "WORKFLOW_NOT_FOUND"
	CodeWorkflowAlreadyExists    = "WORKFLOW_ALREADY_EXISTS"
	CodeWorkflowNameRequired     = "WORKFLOW_NAME_REQUIRED"
	CodeWorkflowVersionNotFound  = "WORKFLOW_VERSION_NOT_FOUND"
	CodeWorkflowVersionConflict  = "WORKFLOW_VERSION_CONFLICT"
	CodeWorkflowVersionImmutable = "WORKFLOW_VERSION_IMMUTABLE"
	CodeWorkflowInvalidDAG       = "WORKFLOW_INVALID_DAG"
	CodeWorkflowCycleDetected    = "WORKFLOW_CYCLE_DETECTED"
	CodeWorkflowNodeInvalid      = "WORKFLOW_NODE_INVALID"
	CodeWorkflowEdgeInvalid      = "WORKFLOW_EDGE_INVALID"
	CodeValidationError          = "VALIDATION_ERROR"
	CodeInvalidRequestBody       = "INVALID_REQUEST_BODY"
	CodeInvalidPathParam         = "INVALID_PATH_PARAMETER"
	CodeInvalidQueryParam        = "INVALID_QUERY_PARAMETER"
	CodeInternalServerError      = "INTERNAL_SERVER_ERROR"
	CodeNotFound                 = "NOT_FOUND"
	CodeConflict                 = "CONFLICT"

	CodeRunNotFound         = "RUN_NOT_FOUND"
	CodeRunAlreadyRunning   = "RUN_ALREADY_RUNNING"
	CodeRunAlreadyCompleted = "RUN_ALREADY_COMPLETED"
	CodeStepNotFound        = "STEP_NOT_FOUND"
	CodeAIInvalidResponse   = "AI_INVALID_RESPONSE"
	CodeAIGenerationFailed  = "AI_GENERATION_FAILED"
	CodeRateLimitExceeded   = "RATE_LIMIT_EXCEEDED"
)

type Envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data"`
	Meta    any    `json:"meta"`
	Error   *Error `json:"error"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details"`
}

type Pagination struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"pageSize"`
	TotalItems int64 `json:"totalItems"`
	TotalPages int   `json:"totalPages"`
	HasNext    bool  `json:"hasNext"`
	HasPrev    bool  `json:"hasPrev"`
}

type List struct {
	Items      any        `json:"items"`
	Pagination Pagination `json:"pagination"`
}

func NewPagination(page, pageSize int, totalItems int64) Pagination {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	totalPages := int(math.Ceil(float64(totalItems) / float64(pageSize)))
	if totalItems == 0 {
		totalPages = 0
	}

	return Pagination{
		Page:       page,
		PageSize:   pageSize,
		TotalItems: totalItems,
		TotalPages: totalPages,
		HasNext:    page < totalPages,
		HasPrev:    page > 1 && totalPages > 0,
	}
}

func writeJSON(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

func OK(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, Envelope{
		Success: true,
		Data:    data,
		Meta:    nil,
		Error:   nil,
	})
}

func Created(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusCreated, Envelope{
		Success: true,
		Data:    data,
		Meta:    nil,
		Error:   nil,
	})
}

// Accepted acknowledges a request that is durably recorded and will be
// processed asynchronously (e.g. a triggered workflow run).
func Accepted(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusAccepted, Envelope{
		Success: true,
		Data:    data,
		Meta:    nil,
		Error:   nil,
	})
}

func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func Fail(w http.ResponseWriter, status int, code, msg string) {
	FailWithDetails(w, status, code, msg, nil)
}

func FailWithDetails(w http.ResponseWriter, status int, code, msg string, details any) {
	writeJSON(w, status, Envelope{
		Success: false,
		Data:    nil,
		Meta:    nil,
		Error: &Error{
			Code:    code,
			Message: msg,
			Details: details,
		},
	})
}
