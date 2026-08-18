package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
	"flowforge/internal/platform/eventstream"
	"flowforge/internal/platform/httpx"
	"flowforge/internal/platform/metrics"
	"flowforge/internal/platform/webhookauth"
	"flowforge/internal/workflow"
)

// ExecutionHandler serves the run lifecycle, inspection, and analysis API.
type ExecutionHandler struct {
	useCase ExecutionUseCase
	clients eventstream.ClientManager
	metrics *metrics.Metrics
}

func NewExecutionHandler(useCase ExecutionUseCase) *ExecutionHandler {
	return NewExecutionHandlerWithStream(useCase, eventstream.NewNoopClientManager(), nil)
}

func NewExecutionHandlerWithStream(useCase ExecutionUseCase, clients eventstream.ClientManager, m *metrics.Metrics) *ExecutionHandler {
	if clients == nil {
		clients = eventstream.NewNoopClientManager()
	}
	return &ExecutionHandler{
		useCase: useCase,
		clients: clients,
		metrics: m,
	}
}

type triggerRunRequest struct {
	TriggerSource string          `json:"triggerSource"`
	Input         json.RawMessage `json:"input"`
}

type retryRunRequest struct {
	RetryFailedStepsOnly bool `json:"retryFailedStepsOnly"`
}

type analyzeRunRequest struct {
	IncludeLogs *bool `json:"includeLogs"`
	LogLimit    int   `json:"logLimit"`
}

type inboundEventRequest struct {
	CorrelationKey string          `json:"correlationKey"`
	Payload        json.RawMessage `json:"payload"`
}

// TriggerRun starts a run of a workflow's current published version.
func (h *ExecutionHandler) TriggerRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	var req triggerRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// api-3.md §10.1.1: triggerSource must be "manual" for the MVP if supplied.
	if req.TriggerSource != "" && req.TriggerSource != "manual" {
		httpx.Fail(w, http.StatusUnprocessableEntity, httpx.CodeValidationError,
			"triggerSource must be \"manual\"")
		return
	}

	var idemKey *string
	if v := r.Header.Get("Idempotency-Key"); v != "" {
		idemKey = &v
	}

	run, err := h.useCase.CreateRun(r.Context(), CreateRunCommand{
		TenantID:       authUser.TenantID,
		WorkflowID:     wfID,
		ActorID:        authUser.ID,
		TriggerType:    req.TriggerSource,
		InputContext:   req.Input,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.Accepted(w, newRunView(run))
}

// ListRuns lists a workflow's runs, newest first.
func (h *ExecutionHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	wfID, err := uuid.Parse(r.PathValue("workflowId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid workflow ID format")
		return
	}

	q := r.URL.Query()
	from, err := parseOptionalTime(q.Get("createdAtFrom"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidQueryParam, "invalid createdAtFrom parameter")
		return
	}
	to, err := parseOptionalTime(q.Get("createdAtTo"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidQueryParam, "invalid createdAtTo parameter")
		return
	}

	res, err := h.useCase.ListRuns(r.Context(), ListRunsQuery{
		TenantID:      authUser.TenantID,
		WorkflowID:    wfID,
		Status:        q.Get("status"),
		TriggerType:   q.Get("triggerType"),
		CreatedAtFrom: from,
		CreatedAtTo:   to,
		Page:          atoiDefault(q.Get("page"), 1),
		PageSize:      atoiDefault(q.Get("pageSize"), 20),
	})
	if err != nil {
		h.handleError(w, err)
		return
	}

	items := make([]map[string]any, 0, len(res.Items))
	for _, run := range res.Items {
		items = append(items, newRunView(run))
	}
	httpx.OK(w, httpx.List{
		Items:      items,
		Pagination: httpx.NewPagination(res.Page, res.PageSize, res.TotalItems),
	})
}

// GetRun returns one run's detail.
func (h *ExecutionHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}
	run, err := h.useCase.GetRun(r.Context(), authUser.TenantID, runID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.OK(w, newRunView(run))
}

// CancelRun cancels a running run.
func (h *ExecutionHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}
	if err := h.useCase.CancelRun(r.Context(), authUser.TenantID, runID); err != nil {
		h.handleError(w, err)
		return
	}
	httpx.Accepted(w, map[string]any{"runId": runID, "status": domain.RunStatusCanceled})
}

// RetryRun retries a finished run.
func (h *ExecutionHandler) RetryRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}

	var req retryRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var idemKey *string
	if v := r.Header.Get("Idempotency-Key"); v != "" {
		idemKey = &v
	}

	run, err := h.useCase.RetryRun(r.Context(), RetryRunCommand{
		TenantID:             authUser.TenantID,
		RunID:                runID,
		ActorID:              authUser.ID,
		RetryFailedStepsOnly: req.RetryFailedStepsOnly,
		IdempotencyKey:       idemKey,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.Accepted(w, map[string]any{
		"runId":         run.ID,
		"originalRunId": run.RetriedFromRunID,
		"status":        run.Status,
	})
}

// ListSteps lists a run's steps.
func (h *ExecutionHandler) ListSteps(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}
	steps, err := h.useCase.ListSteps(r.Context(), authUser.TenantID, runID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		views = append(views, newStepSummaryView(s))
	}
	httpx.OK(w, views)
}

// GetStep returns one step of a run.
func (h *ExecutionHandler) GetStep(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}
	stepRunID, err := uuid.Parse(r.PathValue("stepRunId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid step run ID format")
		return
	}
	step, err := h.useCase.GetStep(r.Context(), authUser.TenantID, runID, stepRunID)
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.OK(w, newStepDetailView(step))
}

// newStepSummaryView renders ListSteps's documented shape (api-3.md §10.2.1) —
// no payloads.
func newStepSummaryView(s *domain.StepRun) map[string]any {
	return map[string]any{
		"stepRunId":      s.ID,
		"workflowNodeId": s.WorkflowNodeID,
		"nodeKey":        s.NodeKey,
		"status":         s.Status,
		"attemptCount":   s.AttemptCount,
		"startedAt":      s.StartedAt,
		"finishedAt":     s.FinishedAt,
	}
}

// newStepDetailView renders GetStep's documented shape (api-3.md §10.2.2) —
// includes payloads.
func newStepDetailView(s *domain.StepRun) map[string]any {
	return map[string]any{
		"stepRunId":      s.ID,
		"workflowNodeId": s.WorkflowNodeID,
		"nodeKey":        s.NodeKey,
		"status":         s.Status,
		"attemptCount":   s.AttemptCount,
		"inputPayload":   s.InputPayload,
		"outputPayload":  s.OutputPayload,
		"errorPayload":   s.ErrorPayload,
		"startedAt":      s.StartedAt,
		"finishedAt":     s.FinishedAt,
	}
}

// ListLogs lists a run's execution logs, newest first.
func (h *ExecutionHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}

	q := r.URL.Query()
	res, err := h.useCase.ListLogs(r.Context(), ListLogsQuery{
		TenantID:  authUser.TenantID,
		RunID:     runID,
		Level:     q.Get("level"),
		StepRunID: q.Get("stepRunId"),
		Page:      atoiDefault(q.Get("page"), 1),
		PageSize:  atoiDefault(q.Get("pageSize"), 20),
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.OK(w, httpx.List{
		Items:      res.Items,
		Pagination: httpx.NewPagination(res.Page, res.PageSize, res.TotalItems),
	})
}

// AnalyzeRun generates AI failure analysis for a run.
func (h *ExecutionHandler) AnalyzeRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid run ID format")
		return
	}

	var req analyzeRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	includeLogs := true
	if req.IncludeLogs != nil {
		includeLogs = *req.IncludeLogs
	}

	res, err := h.useCase.AnalyzeRun(r.Context(), AnalyzeRunCommand{
		TenantID:    authUser.TenantID,
		RunID:       runID,
		IncludeLogs: includeLogs,
		LogLimit:    req.LogLimit,
	})
	if err != nil {
		h.handleError(w, err)
		return
	}
	httpx.OK(w, map[string]any{
		"runId":         res.RunID,
		"diagnosis":     res.Diagnosis,
		"possibleCause": res.PossibleCause,
		"suggestedFix":  res.SuggestedFix,
		"confidence":    res.Confidence,
	})
}

func (h *ExecutionHandler) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRunNotFound):
		httpx.Fail(w, http.StatusNotFound, httpx.CodeRunNotFound, "requested workflow run was not found")
	case errors.Is(err, ErrStepNotFound):
		httpx.Fail(w, http.StatusNotFound, httpx.CodeStepNotFound, "requested step run was not found")
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		httpx.Fail(w, http.StatusNotFound, httpx.CodeWorkflowNotFound, "requested workflow was not found")
	case errors.Is(err, ErrRunAlreadyRunning):
		httpx.Fail(w, http.StatusConflict, httpx.CodeRunAlreadyRunning, "run is already running or pending")
	case errors.Is(err, ErrRunAlreadyCompleted):
		httpx.Fail(w, http.StatusConflict, httpx.CodeRunAlreadyCompleted, "run already reached a terminal status")
	case errors.Is(err, ErrRunIllegalCancel):
		httpx.Fail(w, http.StatusConflict, httpx.CodeRunAlreadyCompleted, "cannot cancel a run that is not running")
	case errors.Is(err, ErrNoPublishedVersion):
		httpx.Fail(w, http.StatusConflict, httpx.CodeWorkflowVersionConflict, "workflow has no published version to run")
	case errors.Is(err, ErrAIInvalidResponse):
		httpx.Fail(w, http.StatusConflict, httpx.CodeAIInvalidResponse, "ai provider returned an invalid response")
	case errors.Is(err, ErrAIGenerationFailed):
		httpx.Fail(w, http.StatusServiceUnavailable, httpx.CodeAIGenerationFailed, "ai provider failed to generate a response")
	default:
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")
	}
}

// decodeJSON decodes r.Body into dst, writing an error response and returning
// false on failure. An oversized body is 413, malformed JSON is 400.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			httpx.Fail(w, http.StatusRequestEntityTooLarge, httpx.CodeInvalidRequestBody, "request body exceeds maximum allowed size")
			return false
		}
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return false
	}
	return true
}

func newRunView(run *domain.WorkflowRun) map[string]any {
	return map[string]any{
		"runId":             run.ID,
		"workflowId":        run.WorkflowID,
		"workflowVersionId": run.WorkflowVersionID,
		"status":            run.Status,
		"triggerType":       run.TriggerType,
		"retriedFromRunId":  run.RetriedFromRunID,
		"createdAt":         run.CreatedAt,
		"startedAt":         run.StartedAt,
		"finishedAt":        run.FinishedAt,
	}
}

func parseOptionalTime(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func atoiDefault(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	var n int
	for _, c := range v {
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return fallback
	}
	return n
}

// IngestEvent is the HTTP webhook ingress (the baseline transport of the three
// event-delivery adapters). The tenant's webhook secret signs the body; the
// signature is verified here, before HandleEvent is ever reached. Once the
// signature is valid the response is always 200 — match, no-match, and duplicate
// are internal outcomes and must never invite sender-side retry guessing.
func (h *ExecutionHandler) IngestEvent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	tenantID, err := uuid.Parse(r.PathValue("tenantId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid tenant ID format")
		return
	}

	secret, err := h.useCase.GetWebhookSecret(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")
		return
	}
	if secret == "" {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "tenant has no webhook secret configured")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "unable to read request body")
		return
	}
	if !webhookauth.VerifyHMAC(secret, body, r.Header.Get("X-Signature")) {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "invalid signature")
		return
	}

	var req inboundEventRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidRequestBody, "invalid JSON body")
		return
	}

	resolved, err := h.useCase.HandleEvent(r.Context(), tenantID, req.CorrelationKey, req.Payload)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")
		return
	}
	if !resolved {
		// Dead-letter: an event that matched no token must never vanish silently.
		_ = h.useCase.RecordOrphanEvent(r.Context(), tenantID, req.CorrelationKey, req.Payload, "no matching wait token")
	}
	httpx.OK(w, map[string]any{"accepted": true})
}

// RotateWebhookSecret issues a fresh signing secret for a tenant.
func (h *ExecutionHandler) RotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}
	tenantID, err := uuid.Parse(r.PathValue("tenantId"))
	if err != nil {
		httpx.Fail(w, http.StatusBadRequest, httpx.CodeInvalidPathParam, "invalid tenant ID format")
		return
	}
	if tenantID != authUser.TenantID {
		httpx.Fail(w, http.StatusForbidden, httpx.CodeAuthForbidden, "not authorized for this tenant")
		return
	}
	secret, err := h.useCase.RotateWebhookSecret(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "internal server error")
		return
	}
	httpx.OK(w, map[string]any{"webhookSecret": secret})
}

// StreamEvents streams real-time execution events for the authenticated tenant over Server-Sent Events (SSE).
func (h *ExecutionHandler) StreamEvents(w http.ResponseWriter, r *http.Request) {
	authUser, ok := auth.AuthUserFromContext(r.Context())
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, httpx.CodeAuthUnauthorized, "authentication required")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Fail(w, http.StatusInternalServerError, httpx.CodeInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	connID, events, unregister := h.clients.Register(authUser.TenantID)
	defer unregister()

	if h.metrics != nil {
		h.metrics.SSEConnections.WithLabelValues(authUser.TenantID.String()).Inc()
	}
	defer func() {
		if h.metrics != nil {
			h.metrics.SSEConnections.WithLabelValues(authUser.TenantID.String()).Dec()
		}
	}()

	// Send initial comment with connID
	_, _ = fmt.Fprintf(w, ": connected connId=%s\n\n", connID)
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-heartbeat.C:
			writeSSE(w, domain.Event{
				Type:      domain.EventHeartbeat,
				TenantID:  authUser.TenantID,
				Timestamp: time.Now().UTC(),
			})
			flusher.Flush()
		}
	}
}

func writeSSE(w io.Writer, ev domain.Event) {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	data, _ := json.Marshal(ev)
	_, _ = fmt.Fprintf(w, "event: %s\nid: %s\ndata: %s\n\n", ev.Type, ev.ID, string(data))
}
