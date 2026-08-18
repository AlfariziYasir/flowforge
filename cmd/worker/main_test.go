package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"flowforge/internal/engine"
	"flowforge/internal/execution"
	"flowforge/internal/platform/config"
)

func TestHostname_NeverEmpty(t *testing.T) {
	h := hostname()
	assert.NotEmpty(t, h, "hostname() must never return empty string")
}

func TestIsProductionLike(t *testing.T) {
	assert.True(t, isProductionLike("production"))
	assert.True(t, isProductionLike("staging"))
	assert.False(t, isProductionLike("development"))
	assert.False(t, isProductionLike("test"))
	assert.False(t, isProductionLike(""))
}

func TestCoordinatorConfig_Construction(t *testing.T) {
	cfg := &config.Config{
		WorkerConcurrency: 10,
		RunLeaseDuration:  30 * time.Second,
		StepMaxBodyBytes:  1024 * 1024,
	}

	coordCfg := execution.CoordinatorConfig{
		Concurrency:  cfg.WorkerConcurrency,
		Lease:        cfg.RunLeaseDuration,
		Retry:        engine.DefaultRetryPolicy(),
		Timeout:      engine.DefaultTimeoutPolicy(),
		MaxBodyBytes: cfg.StepMaxBodyBytes,
		WorkerID:     "worker-" + hostname(),
	}

	assert.Equal(t, 10, coordCfg.Concurrency)
	assert.Equal(t, 30*time.Second, coordCfg.Lease)
	assert.NotEmpty(t, coordCfg.WorkerID)
	assert.Equal(t, int64(1024*1024), coordCfg.MaxBodyBytes)
}
