package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"flowforge/internal/domain"
)

// EventService implements the event-ingress port (HandleEvent + orphan
// dead-lettering) without the run-lifecycle or AI dependencies the full usecase
// carries. The worker builds one directly from its repos; the usecase embeds one
// so the HTTP/gRPC/NATS adapters all share identical logic.
type EventService struct {
	tokens   WaitTokenRepository
	steps    StepRunRepository
	queue    RunEnqueuer
	txRunner TxRunner
	logger   *slog.Logger
}

// NewEventService wires the event ingress port.
func NewEventService(tokens WaitTokenRepository, steps StepRunRepository, queue RunEnqueuer,
	txRunner TxRunner, logger *slog.Logger) *EventService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EventService{tokens: tokens, steps: steps, queue: queue, txRunner: txRunner, logger: logger}
}

// HandleEvent is the single business-logic entry point for every ingress
// transport (HTTP webhook, gRPC, NATS). It resolves the wait token for the
// correlation key atomically: consume the token (a redelivery is a safe no-op),
// write the event payload as the parked step's output, succeed the step, and
// re-enqueue the run. resolved=false means no matching token — the caller
// dead-letters the event as an orphan.
func (s *EventService) HandleEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte) (bool, error) {
	if correlationKey == "" {
		return false, fmt.Errorf("handle event: empty correlation key")
	}

	token, err := s.tokens.FindTokenByCorrelationKey(ctx, tenantID, correlationKey)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("handle event: find token: %w", err)
	}
	if token.ConsumedAt != nil {
		return true, nil // duplicate redelivery — already handled
	}

	resolved := false
	err = s.txRunner.ExecuteInTx(ctx, func(txCtx context.Context) error {
		consumed, err := s.tokens.ConsumeTokenIfUnconsumed(txCtx, token.ID)
		if err != nil {
			return err
		}
		if !consumed {
			return nil // a concurrent consumer won; nothing to do
		}
		if err := s.tokens.MarkWaitingStepSucceeded(txCtx, tenantID, token.StepRunID, payload); err != nil {
			return err
		}
		resolved = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrStepNotFound) {
			// The step moved on (e.g. its token expired while the event was in
			// flight). The event matched a token, so it is not an orphan — the
			// run is already on its own path.
			return true, nil
		}
		return false, fmt.Errorf("handle event: resolve token: %w", err)
	}

	if resolved {
		if err := s.queue.EnqueueRun(tenantID, token.WorkflowRunID); err != nil {
			s.logger.Warn("enqueue failed after event resolve, relying on stale-pending reclaim",
				slog.String("runID", token.WorkflowRunID.String()), slog.Any("error", err))
		}
	}
	return resolved, nil
}

// RecordOrphanEvent dead-letters an inbound event that matched no wait token,
// so a correlation-key bug is discoverable and countable rather than dropped.
func (s *EventService) RecordOrphanEvent(ctx context.Context, tenantID uuid.UUID, correlationKey string, payload []byte, reason string) error {
	if s.tokens == nil {
		return errors.New("record orphan event: no wait-token repository")
	}
	return s.tokens.RecordOrphanEvent(ctx, &domain.OrphanEvent{
		TenantID:       tenantID,
		CorrelationKey: correlationKey,
		Payload:        json.RawMessage(payload),
		Reason:         reason,
	})
}

// GetWebhookSecret returns the tenant's event-ingress signing secret, or "" if
// none is configured yet.
func (s *EventService) GetWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	if s.tokens == nil {
		return "", errors.New("get webhook secret: no repository")
	}
	return s.tokens.GetWebhookSecret(ctx, tenantID)
}

// RotateWebhookSecret issues a fresh random signing secret for a tenant.
func (s *EventService) RotateWebhookSecret(ctx context.Context, tenantID uuid.UUID) (string, error) {
	if s.tokens == nil {
		return "", errors.New("rotate webhook secret: no repository")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rotate webhook secret: generate: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := s.tokens.SetWebhookSecret(ctx, tenantID, secret); err != nil {
		return "", err
	}
	return secret, nil
}
