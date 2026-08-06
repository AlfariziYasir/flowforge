package executor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/domain"
	"flowforge/internal/engine"
	"flowforge/internal/execution/executor"
	"flowforge/internal/platform/safehttp"
)

func node(nodeType string, cfg map[string]any) domain.NodeInput {
	raw, _ := json.Marshal(cfg)
	return domain.NodeInput{NodeKey: "n", NodeType: nodeType, Config: raw}
}

func devHTTPClient() *http.Client {
	return safehttp.NewSSRFValidator([]string{"127.0.0.1"}, false).Client(5 * time.Second)
}

func TestCondition_RecordsBooleanResult(t *testing.T) {
	in := executor.Input{
		Node:  node(domain.NodeTypeCondition, map[string]any{"expression": "trigger.amount > 100"}),
		Scope: engine.Scope{Trigger: map[string]any{"amount": 150}},
	}

	out, err := executor.Condition{}.Execute(context.Background(), in)
	require.NoError(t, err)
	res, ok := out.Data["result"].(bool)
	require.True(t, ok, "result must be a bool")
	assert.True(t, res)
}

func TestTransform_RecordsValue(t *testing.T) {
	in := executor.Input{
		Node:  node(domain.NodeTypeTransform, map[string]any{"expression": "trigger.name + \"!\""}),
		Scope: engine.Scope{Trigger: map[string]any{"name": "flow"}},
	}

	out, err := executor.Transform{}.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, "flow!", out.Data["value"])
}

// Q-8: DELAY returns promptly on cancellation, not after its full duration.
func TestDelay_CancellationReturnsPromptly(t *testing.T) {
	in := executor.Input{
		Node: node(domain.NodeTypeDelay, map[string]any{"seconds": 60}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = executor.Delay{}.Execute(ctx, in)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DELAY did not return promptly on cancellation")
	}
}

// Q-9: HTTP interpolates url/headers/body from scope; an unresolvable
// placeholder fails the step.
func TestHTTP_InterpolatesAndFailsOnUnresolvable(t *testing.T) {
	var gotHeader, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Tenant")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	in := executor.Input{
		Node: node(domain.NodeTypeHTTP, map[string]any{
			"method":  "POST",
			"url":     "{{trigger.url}}",
			"headers": map[string]any{"X-Tenant": "{{trigger.tenant}}"},
			"body":    "id={{trigger.id}}",
		}),
		Scope: engine.Scope{Trigger: map[string]any{
			"url": srv.URL, "tenant": "acme", "id": 7,
		}},
		HTTP:         devHTTPClient(),
		MaxBodyBytes: 1 << 20,
	}

	out, err := executor.HTTP{}.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, out.Data["statusCode"])
	assert.Equal(t, "acme", gotHeader)
	assert.Equal(t, "id=7", gotBody)

	// Unresolvable placeholder → step failure, no request sent.
	bad := executor.Input{
		Node: node(domain.NodeTypeHTTP, map[string]any{
			"url": "{{trigger.missing}}",
		}),
		Scope:        engine.Scope{},
		HTTP:         devHTTPClient(),
		MaxBodyBytes: 1 << 20,
	}
	_, err = executor.HTTP{}.Execute(context.Background(), bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolvable")
}

// Q-10: HTTP response body over the cap fails the step without buffering it.
func TestHTTP_OversizedBodyFails(t *testing.T) {
	big := strings.Repeat("x", 64*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, big)
	}))
	defer srv.Close()

	in := executor.Input{
		Node:         node(domain.NodeTypeHTTP, map[string]any{"url": srv.URL}),
		Scope:        engine.Scope{},
		HTTP:         devHTTPClient(),
		MaxBodyBytes: 1024,
	}

	_, err := executor.HTTP{}.Execute(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

// Q-12: an executor honours the coordinator-applied timeout.
func TestHTTP_HonoursContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		fmt.Fprint(w, "late")
	}))
	defer srv.Close()

	in := executor.Input{
		Node:         node(domain.NodeTypeHTTP, map[string]any{"url": srv.URL}),
		Scope:        engine.Scope{},
		HTTP:         devHTTPClient(),
		MaxBodyBytes: 1 << 20,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := executor.HTTP{}.Execute(ctx, in)
	assert.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "must abort on timeout, not wait for the server")
}

// Phase 7: EVENT_WAIT interpolates the correlation key, enforces the per-tenant
// cap, creates a token, and reports Waiting=true so the coordinator releases
// the run instead of holding a goroutine.
func TestEventWait_ParksRun(t *testing.T) {
	var created domain.StepWaitToken
	store := &fakeWaitTokens{
		create: func(ctx context.Context, token domain.StepWaitToken) error {
			created = token
			return nil
		},
		countActive: func(ctx context.Context, tenantID uuid.UUID) (int, error) { return 0, nil },
	}

	in := executor.Input{
		Node:       node(domain.NodeTypeEventWait, map[string]any{"correlationKey": "{{trigger.orderId}}"}),
		Scope:      engine.Scope{Trigger: map[string]any{"orderId": "ORD-123"}},
		TenantID:   uuid.New(),
		RunID:      uuid.New(),
		StepRunID:  uuid.New(),
		WaitTokens: store,
		WaitConfig: executor.WaitConfig{MaxPerTenant: 5, DefaultDuration: time.Hour, MaxDuration: 24 * time.Hour},
	}

	out, err := executor.EventWait{}.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.True(t, out.Waiting, "EVENT_WAIT must signal Waiting so the run is released")
	assert.Equal(t, "ORD-123", created.CorrelationKey)
	assert.Equal(t, in.TenantID, created.TenantID)
	assert.Equal(t, in.RunID, created.WorkflowRunID)
	assert.Equal(t, in.StepRunID, created.StepRunID)
	assert.True(t, created.ExpiresAt.After(time.Now()))
}

func TestEventWait_EnforcesPerTenantCap(t *testing.T) {
	store := &fakeWaitTokens{
		create:      func(ctx context.Context, token domain.StepWaitToken) error { return nil },
		countActive: func(ctx context.Context, tenantID uuid.UUID) (int, error) { return 5, nil },
	}

	in := executor.Input{
		Node:       node(domain.NodeTypeEventWait, map[string]any{"correlationKey": "k"}),
		Scope:      engine.Scope{},
		TenantID:   uuid.New(),
		RunID:      uuid.New(),
		StepRunID:  uuid.New(),
		WaitTokens: store,
		WaitConfig: executor.WaitConfig{MaxPerTenant: 5, DefaultDuration: time.Hour},
	}

	_, err := executor.EventWait{}.Execute(context.Background(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cap")
}

func TestEventWait_MissingCorrelationKeyFails(t *testing.T) {
	store := &fakeWaitTokens{
		create:      func(ctx context.Context, token domain.StepWaitToken) error { return nil },
		countActive: func(ctx context.Context, tenantID uuid.UUID) (int, error) { return 0, nil },
	}
	in := executor.Input{
		Node:       node(domain.NodeTypeEventWait, map[string]any{"correlationKey": "{{trigger.missing}}"}),
		Scope:      engine.Scope{},
		TenantID:   uuid.New(),
		RunID:      uuid.New(),
		StepRunID:  uuid.New(),
		WaitTokens: store,
		WaitConfig: executor.WaitConfig{MaxPerTenant: 5, DefaultDuration: time.Hour},
	}
	_, err := executor.EventWait{}.Execute(context.Background(), in)
	require.Error(t, err)
}

type fakeWaitTokens struct {
	create      func(ctx context.Context, token domain.StepWaitToken) error
	countActive func(ctx context.Context, tenantID uuid.UUID) (int, error)
}

func (f *fakeWaitTokens) CreateToken(ctx context.Context, token domain.StepWaitToken) error {
	if f.create != nil {
		return f.create(ctx, token)
	}
	return nil
}

func (f *fakeWaitTokens) CountActiveTokens(ctx context.Context, tenantID uuid.UUID) (int, error) {
	if f.countActive != nil {
		return f.countActive(ctx, tenantID)
	}
	return 0, nil
}
