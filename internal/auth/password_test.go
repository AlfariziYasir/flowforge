package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"flowforge/internal/auth"
)

func TestPasswordService_HashPassword(t *testing.T) {
	svc := auth.NewPasswordService()

	t.Run("successfully hashes valid password", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		hash, err := svc.HashPassword("SecretP@ss123")
		req.NoError(err)
		is.NotEmpty(hash)
		is.NotEqual("SecretP@ss123", hash)
	})

	t.Run("rejects empty password", func(t *testing.T) {
		is := assert.New(t)

		_, err := svc.HashPassword("")
		is.ErrorIs(err, auth.ErrInvalidPasswordLength)
	})

	t.Run("rejects password shorter than 8 characters", func(t *testing.T) {
		is := assert.New(t)

		_, err := svc.HashPassword("short")
		is.ErrorIs(err, auth.ErrInvalidPasswordLength)
	})

	t.Run("rejects password exceeding 72 bytes", func(t *testing.T) {
		is := assert.New(t)

		over72Bytes := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		_, err := svc.HashPassword(over72Bytes)
		is.ErrorIs(err, auth.ErrInvalidPasswordLength)
	})
}

func TestPasswordService_ComparePassword(t *testing.T) {
	svc := auth.NewPasswordService()

	t.Run("returns nil for matching password", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		hash, err := svc.HashPassword("CorrectHorseBatteryStaple")
		req.NoError(err)

		err = svc.ComparePassword(hash, "CorrectHorseBatteryStaple")
		is.NoError(err)
	})

	t.Run("returns error for mismatched password", func(t *testing.T) {
		is := assert.New(t)
		req := require.New(t)

		hash, err := svc.HashPassword("CorrectPassword")
		req.NoError(err)

		err = svc.ComparePassword(hash, "WrongPassword")
		is.Error(err)
	})
}
