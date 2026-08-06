// Package safehttp provides the outbound-HTTP security boundary for executors.
//
// The SSRF validator is a denylist applied AFTER DNS resolution: a hostname is
// safe only if every address it resolves to is public. Checking the hostname
// string before resolution is a DNS-rebinding hole, and pinning the dialled IP
// to the validated one closes the TOCTOU gap between validation and dial.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// ipResolver is the DNS seam. Production code uses net.DefaultResolver; tests
// inject a fake to prove validation happens after resolution.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// SSRFValidator rejects outbound destinations that resolve to non-public
// addresses: loopback, private ranges (RFC 1918 / RFC 4193), link-local
// (including the 169.254.169.254 cloud-metadata address), multicast, and
// unspecified.
//
// allowedHosts is an OPT-IN development override that bypasses the denylist.
// It is ignored entirely when productionLike is true: production can never be
// weakened by configuration.
type SSRFValidator struct {
	allowed        map[string]struct{}
	productionLike bool
	resolver       ipResolver
}

// NewSSRFValidator builds a validator. allowedHosts may be empty; productionLike
// disables the allowlist regardless of its contents.
func NewSSRFValidator(allowedHosts []string, productionLike bool) *SSRFValidator {
	return newSSRFValidator(allowedHosts, productionLike, net.DefaultResolver)
}

func newSSRFValidator(allowedHosts []string, productionLike bool, resolver ipResolver) *SSRFValidator {
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, h := range allowedHosts {
		if h = strings.TrimSpace(h); h != "" {
			allowed[strings.ToLower(h)] = struct{}{}
		}
	}
	return &SSRFValidator{allowed: allowed, productionLike: productionLike, resolver: resolver}
}

// ValidateHostname resolves host and rejects it if ANY resolved address is
// non-public (fail closed). A literal IP is checked directly without a lookup.
func (v *SSRFValidator) ValidateHostname(ctx context.Context, host string) error {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	if host == "" {
		return fmt.Errorf("ssrf: empty host")
	}

	// Dev-only opt-in: an allowlisted host bypasses the denylist entirely,
	// including the literal-IP path below.
	if !v.productionLike {
		if _, ok := v.allowed[strings.ToLower(host)]; ok {
			return nil
		}
	}

	if ip := net.ParseIP(host); ip != nil {
		return v.validateIP(ip)
	}

	addrs, err := v.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("ssrf: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("ssrf: resolve %q: no addresses", host)
	}
	for _, a := range addrs {
		if err := v.validateIP(a.IP); err != nil {
			return err
		}
	}
	return nil
}

// extraDenied are ranges netip's helpers do not classify but which are
// non-public in practice. 100.64.0.0/10 matters most: AWS EKS pod networking,
// GCP, and several managed-Kubernetes providers place internal endpoints there.
var extraDenied = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, RFC 6598
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network", RFC 1122
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking, RFC 2544
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64 — can embed any IPv4
}

func (v *SSRFValidator) validateIP(ip net.IP) error {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("ssrf: unparseable address %q", ip)
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return fmt.Errorf("ssrf: destination resolves to non-public address %s", addr)
	}
	for _, p := range extraDenied {
		if p.Contains(addr) {
			return fmt.Errorf("ssrf: destination resolves to non-public address %s", addr)
		}
	}
	return nil
}

// Client returns an *http.Client whose connections are pinned to an address
// validated after resolution, and whose redirects are re-validated on every hop.
func (v *SSRFValidator) Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("ssrf: %w", err)
				}
				pinned, err := v.resolveAndPin(ctx, host)
				if err != nil {
					return nil, err
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(pinned, port))
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("ssrf: too many redirects")
			}
			return v.ValidateHostname(req.Context(), req.URL.Hostname())
		},
	}
}

// resolveAndPin resolves host, validates EVERY address (fail closed: one
// non-public address rejects the whole host), and returns one allowed address
// to dial. The dialled IP is therefore the IP that was validated.
func (v *SSRFValidator) resolveAndPin(ctx context.Context, host string) (string, error) {
	// Dev-only opt-in: an allowlisted host is dialed as-is, bypassing the denylist.
	if !v.productionLike {
		if _, ok := v.allowed[strings.ToLower(host)]; ok {
			return host, nil
		}
	}

	if ip := net.ParseIP(host); ip != nil {
		if err := v.validateIP(ip); err != nil {
			return "", err
		}
		return host, nil
	}

	addrs, err := v.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", fmt.Errorf("ssrf: resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("ssrf: resolve %q: no addresses", host)
	}
	// Fail closed, exactly like ValidateHostname: if any address is non-public
	// the host is rejected, so the two checks can never disagree.
	for _, a := range addrs {
		if err := v.validateIP(a.IP); err != nil {
			return "", err
		}
	}
	return addrs[0].IP.String(), nil
}
