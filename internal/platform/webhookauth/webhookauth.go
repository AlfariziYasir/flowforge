// Package webhookauth is the single home of event-ingress signature verification.
// Every transport (HTTP webhook header, gRPC metadata, NATS message headers)
// calls VerifyHMAC here so the security-critical comparison logic exists in
// exactly one place and is tested once.
package webhookauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// VerifyHMAC reports whether sig is the HMAC-SHA256(secret, body) of body.
// Comparison is constant-time (hmac.Equal), never plain ==, so a timing probe
// cannot learn the digest.
func VerifyHMAC(secret string, body []byte, sig string) bool {
	if secret == "" || sig == "" {
		return false
	}
	expected, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(expected, mac.Sum(nil))
}
