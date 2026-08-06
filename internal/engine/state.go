package engine

import (
	"encoding/json"
	"math/rand"
	"time"

	"flowforge/internal/domain"
)

// Step execution statuses written verbatim to step_runs.status.
// These MUST match the CHECK constraint as amended by migration 000002:
//
//	CHECK (status IN ('pending','ready','running','waiting','succeeded','failed','retrying','skipped'))
const (
	StepStatusPending   = "pending"
	StepStatusReady     = "ready"
	StepStatusRunning   = "running"
	StepStatusWaiting   = "waiting"
	StepStatusSucceeded = "succeeded"
	StepStatusFailed    = "failed"
	StepStatusRetrying  = "retrying"
	StepStatusSkipped   = "skipped"
)

// IsTerminal reports whether status is a terminal execution state.
func IsTerminal(status string) bool {
	switch status {
	case StepStatusSucceeded, StepStatusFailed, StepStatusSkipped:
		return true
	default:
		return false
	}
}

// CanTransition reports whether transitioning from status 'from' to 'to' is valid.
func CanTransition(from, to string) bool {
	switch from {
	case StepStatusPending:
		return to == StepStatusReady || to == StepStatusSkipped
	case StepStatusReady:
		return to == StepStatusRunning || to == StepStatusSkipped
	case StepStatusRunning:
		return to == StepStatusSucceeded || to == StepStatusFailed || to == StepStatusRetrying || to == StepStatusWaiting
	case StepStatusWaiting:
		return to == StepStatusSucceeded || to == StepStatusFailed || to == StepStatusSkipped
	case StepStatusRetrying:
		return to == StepStatusRunning || to == StepStatusFailed || to == StepStatusSkipped
	default:
		return false
	}
}

// RetryPolicy defines backoff rules for step retries.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy returns standard retry defaults.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   1 * time.Second,
		MaxDelay:    30 * time.Second,
	}
}

// NextBackoff returns the delay before attempt n (1-indexed), exponential with full jitter.
func (p RetryPolicy) NextBackoff(attempt int, rnd *rand.Rand) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}

	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}

	baseDelay := p.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 1 * time.Second
	}

	// Exponential backoff: baseDelay * 2^(attempt - 1)
	temp := float64(baseDelay) * float64(uint(1)<<uint(min(attempt-1, 30)))
	if temp > float64(maxDelay) {
		temp = float64(maxDelay)
	}

	if rnd == nil {
		return time.Duration(temp)
	}

	// Full jitter: random duration in [0, temp)
	sleep := rnd.Float64() * temp
	return time.Duration(sleep)
}

const (
	DefaultStepTimeout = 30 * time.Second
	MaxStepTimeout     = 15 * time.Minute
)

// TimeoutPolicy defines step timeout defaults and caps.
type TimeoutPolicy struct {
	StepTimeout time.Duration
}

// DefaultTimeoutPolicy returns standard step timeout defaults.
func DefaultTimeoutPolicy() TimeoutPolicy {
	return TimeoutPolicy{
		StepTimeout: DefaultStepTimeout,
	}
}

type timeoutConfig struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
}

// EffectiveTimeout resolves a node's effective timeout bounded by MaxStepTimeout.
func (p TimeoutPolicy) EffectiveTimeout(node domain.NodeInput) (time.Duration, error) {
	defaultTimeout := p.StepTimeout
	if defaultTimeout <= 0 {
		defaultTimeout = DefaultStepTimeout
	}

	if len(node.Config) == 0 {
		if defaultTimeout > MaxStepTimeout {
			return MaxStepTimeout, nil
		}
		return defaultTimeout, nil
	}

	var cfg timeoutConfig
	if err := json.Unmarshal(node.Config, &cfg); err == nil && cfg.TimeoutSeconds > 0 {
		duration := time.Duration(cfg.TimeoutSeconds) * time.Second
		if duration > MaxStepTimeout {
			return MaxStepTimeout, nil
		}
		return duration, nil
	}

	if defaultTimeout > MaxStepTimeout {
		return MaxStepTimeout, nil
	}
	return defaultTimeout, nil
}
