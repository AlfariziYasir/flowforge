package execution_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/workflow"
)

// stubUseCase implements ExecutionUseCase with canned results so the handler's
// transport concerns (decoding, status mapping, error dispatch) are testable in
// isolation.
type stubUseCase struct {
	createErr    error
	getErr       error
	cancelErr    error
	retryErr     error
	analyzeErr   error
	listRunsErr  error
	lastTenant   uuid.UUID
	lastRunID    uuid.UUID
	createdRun   *domain.WorkflowRun
	lastCmd      execution.CreateRunCommand
	lastRetryCmd execution.RetryRunCommand
	step         *domain.StepRun
	secret       string
}

func (s *stubUseCase) CreateRun(ctx context.Context, cmd execution.CreateRunCommand) (*domain.WorkflowRun, error) {
	s.lastCmd = cmd
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.createdRun, nil
}
func (s *stubUseCase) RetryRun(ctx context.Context, cmd execution.RetryRunCommand) (*domain.WorkflowRun, error) {
	s.lastRetryCmd = cmd
	if s.retryErr != nil {
		return nil, s.retryErr
	}
	return s.createdRun, nil
}
func (s *stubUseCase) CancelRun(ctx context.Context, tenantID, runID uuid.UUID) error {
	return s.cancelErr
}
func (s *stubUseCase) GetRun(ctx context.Context, tenantID, runID uuid.UUID) (*domain.WorkflowRun, error) {
	s.lastTenant = tenantID
	s.lastRunID = runID
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.createdRun, nil
}
func (s *stubUseCase) ListRuns(ctx context.Context, q execution.ListRunsQuery) (*execution.PaginatedRuns, error) {
	if s.listRunsErr != nil {
		return nil, s.listRunsErr
	}
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &execution.PaginatedRuns{}, nil
}
func (s *stubUseCase) ListSteps(ctx context.Context, tenantID, runID uuid.UUID) ([]*domain.StepRun, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.step != nil {
		return []*domain.StepRun{s.step}, nil
	}
	return nil, nil
}
func (s *stubUseCase) GetStep(ctx context.Context, tenantID, runID, stepRunID uuid.UUID) (*domain.StepRun, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.step != nil {
		return s.step, nil
	}
	return &domain.StepRun{}, nil
}
func (s *stubUseCase) ListLogs(ctx context.Context, q execution.ListLogsQuery) (*execution.PaginatedLogs, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return &execution.PaginatedLogs{}, nil
}
func (s *stubUseCase) AnalyzeRun(ctx context.Context, cmd execution.AnalyzeRunCommand) (*execution.AnalysisResult, error) {
	if s.analyzeErr != nil {
		return nil, s.analyzeErr
	}
	return &execution.AnalysisResult{RunID: cmd.RunID, Diagnosis: "d", PossibleCause: "p", SuggestedFix: "f", Confidence: 0.8}, nil
}

func (s *stubUseCase) HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (bool, error) {
	return true, nil
}

func (s *stubUseCase) RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error {
	return nil
}

func (s *stubUseCase) GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	if s.secret != "" {
		return s.secret, nil
	}
	return "", nil
}

func (s *stubUseCase) RotateWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	return "rotated-secret", nil
}

func authCtx(tenantID, userID uuid.UUID, role string) context.Context {
	return auth.ContextWithAuthUser(context.Background(), auth.AuthUser{ID: userID, TenantID: tenantID, Role: role})
}

// muxFor registers the exact route patterns Step 9 wires into NewRouter, so
// PathValue works and the two path namespaces are exercised for real.
func muxFor(h *execution.ExecutionHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/workflows/{workflowId}/runs", h.TriggerRun)
	mux.HandleFunc("GET /api/v1/workflows/{workflowId}/runs", h.ListRuns)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}", h.GetRun)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/cancel", h.CancelRun)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/retry", h.RetryRun)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/steps", h.ListSteps)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/steps/{stepRunId}", h.GetStep)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/logs", h.ListLogs)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/analysis", h.AnalyzeRun)
	return mux
}

func doRequest(mux http.Handler, method, path string, body []byte, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodedBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func TestExecutionHandler_ErrorDispatch(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: runID, TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"run not found", execution.ErrRunNotFound, http.StatusNotFound, "RUN_NOT_FOUND"},
		{"step not found", execution.ErrStepNotFound, http.StatusNotFound, "STEP_NOT_FOUND"},
		{"run already running", execution.ErrRunAlreadyRunning, http.StatusConflict, "RUN_ALREADY_RUNNING"},
		{"run already completed", execution.ErrRunAlreadyCompleted, http.StatusConflict, "RUN_ALREADY_COMPLETED"},
		{"illegal cancel", execution.ErrRunIllegalCancel, http.StatusConflict, "RUN_ALREADY_COMPLETED"},
		{"no published version", execution.ErrNoPublishedVersion, http.StatusConflict, "WORKFLOW_VERSION_CONFLICT"},
		{"ai invalid response", execution.ErrAIInvalidResponse, http.StatusConflict, "AI_INVALID_RESPONSE"},
		{"ai generation failed", execution.ErrAIGenerationFailed, http.StatusServiceUnavailable, "AI_GENERATION_FAILED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub.getErr = tc.err
			rec := doRequest(muxFor(h), http.MethodGet, "/api/v1/workflow-runs/"+runID.String(), nil,
				authCtx(tenantID, uuid.New(), "viewer"))
			assert.Equal(t, tc.wantStatus, rec.Code)
			body := decodedBody(t, rec)
			errObj := body["error"].(map[string]any)
			assert.Equal(t, tc.wantCode, errObj["code"])
		})
	}
}

func TestExecutionHandler_GetRun_TenantIsolation(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: runID, TenantID: tenantID, Status: domain.RunStatusRunning}}
	h := execution.NewExecutionHandler(stub)

	rec := doRequest(muxFor(h), http.MethodGet, "/api/v1/workflow-runs/"+runID.String(), nil,
		authCtx(tenantID, uuid.New(), "viewer"))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, tenantID, stub.lastTenant, "the authenticated tenant must be forwarded, never the caller's choice")
}

func TestExecutionHandler_Validation(t *testing.T) {
	tenantID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)

	t.Run("malformed run ID yields 400", func(t *testing.T) {
		rec := doRequest(muxFor(h), http.MethodGet, "/api/v1/workflow-runs/not-a-uuid", nil,
			authCtx(tenantID, uuid.New(), "viewer"))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "INVALID_PATH_PARAMETER", decodedBody(t, rec)["error"].(map[string]any)["code"])
	})

	t.Run("malformed trigger body yields 400", func(t *testing.T) {
		rec := doRequest(muxFor(h), http.MethodPost, "/api/v1/workflows/"+uuid.New().String()+"/runs", []byte("{not json"),
			authCtx(tenantID, uuid.New(), "editor"))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing auth yields 401", func(t *testing.T) {
		rec := doRequest(muxFor(h), http.MethodGet, "/api/v1/workflow-runs/"+uuid.New().String(), nil, nil)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestExecutionHandler_AnalyzeRun_RoutesViewer(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{}
	h := execution.NewExecutionHandler(stub)

	rec := doRequest(muxFor(h), http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/analysis",
		[]byte(`{"includeLogs":false}`), authCtx(tenantID, uuid.New(), "viewer"))
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodedBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, "d", body["diagnosis"])
}

// TestExecutionHandler_Routes wires the exact route patterns Step 9 registers
// and asserts the two path namespaces reach the right handlers.
func TestExecutionHandler_Routes(t *testing.T) {
	tenantID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/workflows/{workflowId}/runs", h.TriggerRun)
	mux.HandleFunc("GET /api/v1/workflows/{workflowId}/runs", h.ListRuns)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}", h.GetRun)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/cancel", h.CancelRun)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/retry", h.RetryRun)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/steps", h.ListSteps)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/steps/{stepRunId}", h.GetStep)
	mux.HandleFunc("GET /api/v1/workflow-runs/{runId}/logs", h.ListLogs)
	mux.HandleFunc("POST /api/v1/workflow-runs/{runId}/analysis", h.AnalyzeRun)

	tests := []struct {
		name   string
		method string
		path   string
		role   string
	}{
		{"trigger", http.MethodPost, "/api/v1/workflows/" + uuid.New().String() + "/runs", "editor"},
		{"list", http.MethodGet, "/api/v1/workflows/" + uuid.New().String() + "/runs", "viewer"},
		{"get", http.MethodGet, "/api/v1/workflow-runs/" + uuid.New().String(), "viewer"},
		{"cancel", http.MethodPost, "/api/v1/workflow-runs/" + uuid.New().String() + "/cancel", "editor"},
		{"retry", http.MethodPost, "/api/v1/workflow-runs/" + uuid.New().String() + "/retry", "editor"},
		{"steps", http.MethodGet, "/api/v1/workflow-runs/" + uuid.New().String() + "/steps", "viewer"},
		{"step", http.MethodGet, "/api/v1/workflow-runs/" + uuid.New().String() + "/steps/" + uuid.New().String(), "viewer"},
		{"logs", http.MethodGet, "/api/v1/workflow-runs/" + uuid.New().String() + "/logs", "viewer"},
		{"analysis", http.MethodPost, "/api/v1/workflow-runs/" + uuid.New().String() + "/analysis", "viewer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader([]byte(`{}`)))
			req = req.WithContext(authCtx(tenantID, uuid.New(), tc.role))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			assert.NotEqual(t, http.StatusNotFound, rec.Code, "route %s must be registered", tc.path)
		})
	}
}

// Z-2: POSTing the exact body api-3.md documents must reach the usecase with the
// input intact and triggerSource mapped to TriggerType.
func TestTriggerRun_AcceptsDocumentedRequestShape(t *testing.T) {
	tenantID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)

	body := `{"input":{"leadEmail":"user@example.com","leadName":"Jane Doe"},"triggerSource":"manual"}`
	rec := doRequest(muxFor(h), http.MethodPost, "/api/v1/workflows/"+uuid.New().String()+"/runs",
		[]byte(body), authCtx(tenantID, uuid.New(), "editor"))

	assert.Equal(t, http.StatusAccepted, rec.Code, "trigger must be 202 Accepted")
	assert.Equal(t, "manual", stub.lastCmd.TriggerType, "triggerSource must map to TriggerType")
	assert.Contains(t, string(stub.lastCmd.InputContext), "leadEmail", "the caller's input must reach the usecase intact")
	assert.Contains(t, string(stub.lastCmd.InputContext), "user@example.com")
}

// Z-1: TriggerRun, CancelRun, and RetryRun all return 202 Accepted.
func TestTriggerCancelRetry_Return202(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: runID, TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)

	cases := []struct {
		name string
		path string
		body []byte
	}{
		{"trigger", "/api/v1/workflows/" + uuid.New().String() + "/runs", []byte(`{"input":{},"triggerSource":"manual"}`)},
		{"cancel", "/api/v1/workflow-runs/" + runID.String() + "/cancel", []byte(`{}`)},
		{"retry", "/api/v1/workflow-runs/" + runID.String() + "/retry", []byte(`{"retryFailedStepsOnly":false}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(mux, http.MethodPost, tc.path, tc.body, authCtx(tenantID, uuid.New(), "editor"))
			assert.Equal(t, http.StatusAccepted, rec.Code, "%s must be 202 Accepted", tc.name)
		})
	}
}

// Z-3: ListRuns maps the parent workflow's not-found sentinel to 404.
func TestHandler_ListRunsWorkflowNotFound(t *testing.T) {
	tenantID := uuid.New()
	stub := &stubUseCase{listRunsErr: workflow.ErrWorkflowNotFound}
	h := execution.NewExecutionHandler(stub)

	rec := doRequest(muxFor(h), http.MethodGet, "/api/v1/workflows/"+uuid.New().String()+"/runs", nil,
		authCtx(tenantID, uuid.New(), "viewer"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "WORKFLOW_NOT_FOUND", decodedBody(t, rec)["error"].(map[string]any)["code"])
}

// Z-4 / AA-1: both step endpoints expose stepRunId and workflowNodeId, never a
// bare id; GetStep carries payloads under the documented *Payload names, and
// ListSteps carries none.
func TestHandler_StepResponseShape(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	step := &domain.StepRun{
		ID:             uuid.New(),
		WorkflowRunID:  runID,
		WorkflowNodeID: uuid.New(),
		NodeKey:        "n1",
		Status:         engine.StepStatusSucceeded,
		AttemptCount:   2,
		InputPayload:   []byte(`{"a":1}`),
		OutputPayload:  []byte(`{"b":2}`),
		ErrorPayload:   []byte(`{"c":3}`),
	}
	stub := &stubUseCase{step: step}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)

	t.Run("GetStep", func(t *testing.T) {
		rec := doRequest(mux, http.MethodGet, "/api/v1/workflow-runs/"+runID.String()+"/steps/"+step.ID.String(), nil,
			authCtx(tenantID, uuid.New(), "viewer"))
		assert.Equal(t, http.StatusOK, rec.Code)
		data := decodedBody(t, rec)["data"].(map[string]any)
		assert.Equal(t, step.ID.String(), data["stepRunId"])
		assert.Equal(t, step.WorkflowNodeID.String(), data["workflowNodeId"])
		assert.Equal(t, "n1", data["nodeKey"])
		_, hasBareID := data["id"]
		assert.False(t, hasBareID, "response must not use a bare 'id' key")

		// AA-1: payload fields must use the documented *Payload names.
		assert.Equal(t, map[string]any{"a": float64(1)}, data["inputPayload"])
		assert.Equal(t, map[string]any{"b": float64(2)}, data["outputPayload"])
		assert.Equal(t, map[string]any{"c": float64(3)}, data["errorPayload"])
		_, hasShortInput := data["input"]
		_, hasShortOutput := data["output"]
		_, hasShortError := data["error"]
		assert.False(t, hasShortInput, "GetStep must not use the short 'input' key")
		assert.False(t, hasShortOutput)
		assert.False(t, hasShortError)
	})

	t.Run("ListSteps", func(t *testing.T) {
		rec := doRequest(mux, http.MethodGet, "/api/v1/workflow-runs/"+runID.String()+"/steps", nil,
			authCtx(tenantID, uuid.New(), "viewer"))
		assert.Equal(t, http.StatusOK, rec.Code)
		data := decodedBody(t, rec)["data"].([]any)[0].(map[string]any)
		assert.Equal(t, step.ID.String(), data["stepRunId"])
		assert.Equal(t, step.WorkflowNodeID.String(), data["workflowNodeId"])
		_, hasBareID := data["id"]
		assert.False(t, hasBareID)

		// AA-1: ListSteps's documented shape carries no payloads at all.
		for _, key := range []string{"input", "output", "error", "inputPayload", "outputPayload", "errorPayload"} {
			_, present := data[key]
			assert.False(t, present, "ListSteps must not carry payload key %q", key)
		}
	})
}

// Z-8: RetryRun reads Idempotency-Key from the header, not the body.
func TestHandler_RetryRunIdempotencyHeader(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: uuid.New(), TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)

	t.Run("header is honoured", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/retry",
			bytes.NewReader([]byte(`{"retryFailedStepsOnly":false}`)))
		req = req.WithContext(authCtx(tenantID, uuid.New(), "editor"))
		req.Header.Set("Idempotency-Key", "retry-key-1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusAccepted, rec.Code)
		require.NotNil(t, stub.lastRetryCmd.IdempotencyKey)
		assert.Equal(t, "retry-key-1", *stub.lastRetryCmd.IdempotencyKey)
	})

	t.Run("body-only idempotencyKey is ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/retry",
			bytes.NewReader([]byte(`{"idempotencyKey":"body-key"}`)))
		req = req.WithContext(authCtx(tenantID, uuid.New(), "editor"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Nil(t, stub.lastRetryCmd.IdempotencyKey, "a body field is not the documented idempotency channel")
	})
}

// Z-5: RetryRun's success response carries the documented originalRunId field.
func TestHandler_RetryRunResponseShape(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	origID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{
		ID: runID, TenantID: tenantID, Status: domain.RunStatusPending, RetriedFromRunID: &origID,
	}}
	h := execution.NewExecutionHandler(stub)

	rec := doRequest(muxFor(h), http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/retry",
		[]byte(`{}`), authCtx(tenantID, uuid.New(), "editor"))
	assert.Equal(t, http.StatusAccepted, rec.Code)
	data := decodedBody(t, rec)["data"].(map[string]any)
	assert.Equal(t, runID.String(), data["runId"])
	assert.Equal(t, origID.String(), data["originalRunId"], "retry response must expose its lineage")
}

// Z-7: an oversized retry or analysis body is 413, not a flat 400.
func TestHandler_OversizedBodyIs413(t *testing.T) {
	tenantID := uuid.New()
	runID := uuid.New()
	stub := &stubUseCase{createdRun: &domain.WorkflowRun{ID: runID, TenantID: tenantID, Status: domain.RunStatusPending}}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)

	huge := bytes.Repeat([]byte("x"), 1<<20+1)

	t.Run("retry", func(t *testing.T) {
		body := []byte(`{"retryFailedStepsOnly":false,"blob":"` + string(huge) + `"}`)
		rec := doRequest(mux, http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/retry", body,
			authCtx(tenantID, uuid.New(), "editor"))
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("analysis", func(t *testing.T) {
		body := []byte(`{"includeLogs":true,"note":"` + string(huge) + `"}`)
		rec := doRequest(mux, http.MethodPost, "/api/v1/workflow-runs/"+runID.String()+"/analysis", body,
			authCtx(tenantID, uuid.New(), "viewer"))
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})
}

func hmacSig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Phase 7: the webhook ingress rejects a bad signature and accepts a valid one
// (always 200 once the signature is valid — match/no-match/duplicate are
// internal).
func TestHandler_WebhookIngest(t *testing.T) {
	tenantID := uuid.New()
	secret := "tenant-secret"
	stub := &stubUseCase{secret: secret}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)
	mux.HandleFunc("POST /api/v1/tenants/{tenantId}/events", h.IngestEvent)

	body := []byte(`{"correlationKey":"ORD-1","payload":{"paid":true}}`)

	t.Run("invalid signature is rejected before HandleEvent", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID.String()+"/events", bytes.NewReader(body))
		req.Header.Set("X-Signature", hmacSig("wrong-secret", body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("valid signature is accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID.String()+"/events", bytes.NewReader(body))
		req.Header.Set("X-Signature", hmacSig(secret, body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("tenant without a secret is rejected", func(t *testing.T) {
		stub2 := &stubUseCase{secret: ""}
		h2 := execution.NewExecutionHandler(stub2)
		mux2 := muxFor(h2)
		mux2.HandleFunc("POST /api/v1/tenants/{tenantId}/events", h2.IngestEvent)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID.String()+"/events", bytes.NewReader(body))
		req.Header.Set("X-Signature", hmacSig(secret, body))
		rec := httptest.NewRecorder()
		mux2.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// Phase 7: webhook secret rotation is admin-only and returns the new secret.
func TestHandler_RotateWebhookSecret(t *testing.T) {
	tenantID := uuid.New()
	stub := &stubUseCase{}
	h := execution.NewExecutionHandler(stub)
	mux := muxFor(h)
	mux.Handle("POST /api/v1/tenants/{tenantId}/webhook-secret/rotate", http.HandlerFunc(h.RotateWebhookSecret))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tenantID.String()+"/webhook-secret/rotate", nil)
	req = req.WithContext(authCtx(tenantID, uuid.New(), "admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "rotated-secret", decodedBody(t, rec)["data"].(map[string]any)["webhookSecret"])
}
