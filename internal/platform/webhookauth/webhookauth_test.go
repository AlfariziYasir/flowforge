package webhookauth_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"

	"flowforge/internal/platform/webhookauth"
)

func sig(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMAC(t *testing.T) {
	secret := "s3cr3t"
	body := []byte(`{"correlationKey":"ORD-1"}`)
	valid := sig(secret, body)

	t.Run("valid signature accepted", func(t *testing.T) {
		assert.True(t, webhookauth.VerifyHMAC(secret, body, valid))
	})

	t.Run("wrong secret rejected", func(t *testing.T) {
		assert.False(t, webhookauth.VerifyHMAC("other-secret", body, valid))
	})

	t.Run("tampered body rejected", func(t *testing.T) {
		assert.False(t, webhookauth.VerifyHMAC(secret, []byte(`{"correlationKey":"ORD-2"}`), valid))
	})

	t.Run("empty secret or signature rejected", func(t *testing.T) {
		assert.False(t, webhookauth.VerifyHMAC("", body, valid))
		assert.False(t, webhookauth.VerifyHMAC(secret, body, ""))
	})

	t.Run("malformed signature rejected", func(t *testing.T) {
		assert.False(t, webhookauth.VerifyHMAC(secret, body, "not-hex!"))
	})
}
