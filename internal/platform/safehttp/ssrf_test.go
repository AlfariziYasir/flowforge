package safehttp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeResolver struct {
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)
}

func (f fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f.lookup(ctx, host)
}

func addr(ip string) []net.IPAddr {
	return []net.IPAddr{{IP: net.ParseIP(ip)}}
}

// Q-13: every denylist family is rejected.
func TestSSRFValidator_BlocksNonPublicAddresses(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"127.0.0.2",       // loopback range
		"169.254.169.254", // cloud metadata
		"169.254.1.1",     // link-local
		"10.0.0.1",        // RFC 1918
		"192.168.1.1",
		"172.16.0.1",
		"::1",       // IPv6 loopback
		"fc00::1",   // RFC 4193 unique-local
		"0.0.0.0",   // unspecified
		"224.0.0.1", // multicast
		// Y-4: ranges netip's helpers miss but which are non-public in practice.
		"100.64.0.1",      // CGNAT, RFC 6598
		"0.0.0.1",         // "this network", RFC 1122 (non-zero)
		"192.0.0.1",       // IETF protocol assignments
		"198.18.0.1",      // benchmarking, RFC 2544
		"64:ff9b::7f00:1", // NAT64 embedding 127.0.0.1
	}
	v := NewSSRFValidator(nil, false)
	for _, host := range blocked {
		t.Run(host, func(t *testing.T) {
			err := v.ValidateHostname(context.Background(), host)
			assert.Error(t, err, "expected %q to be blocked", host)
		})
	}
}

// Q-17: ordinary public addresses pass — including an IPv6 public address.
func TestSSRFValidator_PermitsPublicAddresses(t *testing.T) {
	v := NewSSRFValidator(nil, false)
	for _, host := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2001:4860:4860::8888"} {
		assert.NoError(t, v.ValidateHostname(context.Background(), host), "public address %q must pass", host)
	}
}

// Y-4: ValidateHostname and resolveAndPin agree, fail closed on multi-address
// hosts — one non-public address rejects the whole host in both paths.
func TestSSRFValidator_ResolveAndPinAgreesWithValidate(t *testing.T) {
	v := newSSRFValidator(nil, false, fakeResolver{lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return append(addr("8.8.8.8"), addr("10.0.0.7")...), nil // one good, one private
	}})

	host := "dual.example.com"
	require.Error(t, v.ValidateHostname(context.Background(), host), "ValidateHostname fails closed")
	_, err := v.resolveAndPin(context.Background(), host)
	require.Error(t, err, "resolveAndPin must fail closed too — the two checks cannot disagree")
	assert.Contains(t, err.Error(), "10.0.0.7")
}

// Q-14: a hostname that RESOLVES to a private address is blocked, even though
// the string itself contains nothing suspicious.
func TestSSRFValidator_BlocksHostnameResolvingToPrivate(t *testing.T) {
	v := newSSRFValidator(nil, false, fakeResolver{lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return addr("10.0.0.7"), nil
	}})

	err := v.ValidateHostname(context.Background(), "innocent.example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10.0.0.7")
}

// Q-15: a redirect from an allowed host to a private address is blocked.
func TestSSRFValidator_BlocksRedirectToPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://secret.internal/private", http.StatusFound)
	}))
	defer srv.Close()

	// Dev mode: loopback is allowlisted so the initial request reaches the test
	// server. The redirect target resolves to 127.0.0.1 and must be rejected.
	v := newSSRFValidator([]string{"127.0.0.1"}, false, fakeResolver{lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return addr("127.0.0.1"), nil
	}})

	client := v.Client(0)
	_, err := client.Get(srv.URL)
	require.Error(t, err, "redirect to 127.0.0.1 must be rejected")
	assert.Contains(t, err.Error(), "non-public")
}

// Q-16: the allowlist is honoured in development and ignored in production.
func TestSSRFValidator_AllowlistIsDevOnly(t *testing.T) {
	t.Run("honoured in development", func(t *testing.T) {
		v := newSSRFValidator([]string{"127.0.0.1", "localhost"}, false, fakeResolver{})
		assert.NoError(t, v.ValidateHostname(context.Background(), "127.0.0.1"))
	})

	t.Run("ignored in production", func(t *testing.T) {
		v := newSSRFValidator([]string{"127.0.0.1", "localhost"}, true, fakeResolver{lookup: func(ctx context.Context, host string) ([]net.IPAddr, error) {
			return addr("127.0.0.1"), nil
		}})
		err := v.ValidateHostname(context.Background(), "localhost")
		require.Error(t, err, "allowlisted loopback must still be blocked in production")
		assert.Contains(t, err.Error(), "non-public")
	})
}
