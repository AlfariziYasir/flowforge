package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"flowforge/internal/domain"
)

// SweepExpiredWaitTokens fails the step of every expired, unconsumed wait token
// and wakes its run back to 'pending' so a worker re-claims it and walks the
// step's error path — a run parked on a token that never resolves must reach a
// terminal state, never hang. Returns the number of tokens swept.
//
// Called from the worker's reaper loop on its existing ticker (Phase 5).
func SweepExpiredWaitTokens(ctx context.Context, tokens WaitTokenRepository, runs RunRepository,
	queue RunEnqueuer, log *slog.Logger) (int, error) {
	expired, err := tokens.FindExpiredTokens(ctx, 100)
	if err != nil {
		return 0, fmt.Errorf("sweep expired wait tokens: %w", err)
	}

	swept := 0
	for _, t := range expired {
		// Always mark the expired token row handled so it never resurfaces in FindExpiredTokens.
		_ = tokens.MarkTokenHandled(ctx, t.TenantID, t.ID)

		// Gated on the step still being 'waiting' — a run a worker already
		// picked up again is left alone.
		if err := tokens.MarkWaitingStepFailed(ctx, t.TenantID, t.StepRunID); err != nil {
			if errors.Is(err, ErrStepNotFound) {
				continue
			}
			return swept, fmt.Errorf("sweep token %s: %w", t.ID, err)
		}
		if err := runs.UpdateRunStatus(ctx, t.TenantID, t.WorkflowRunID, domain.RunStatusPending, false); err != nil {
			return swept, fmt.Errorf("sweep token %s: wake run: %w", t.ID, err)
		}
		if err := queue.EnqueueRun(t.TenantID, t.WorkflowRunID); err != nil {
			log.Warn("enqueue failed after token sweep, relying on stale-pending reclaim",
				slog.String("runID", t.WorkflowRunID.String()), slog.Any("error", err))
		}
		swept++
	}
	if log != nil && swept > 0 {
		log.Warn("expired wait tokens swept", slog.Int("swept", swept))
	}
	return swept, nil
}
