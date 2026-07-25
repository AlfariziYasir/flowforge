package logger_test

import (
	"testing"

	"flowforge/internal/platform/logger"

	"github.com/stretchr/testify/assert"
)

func TestSetup_DevAndProd(t *testing.T) {
	lDev := logger.Setup("development", "debug")
	assert.NotNil(t, lDev)

	lProd := logger.Setup("production", "info")
	assert.NotNil(t, lProd)
}
