// Package netguard keeps user-supplied URLs (alerter targets) from reaching
// the cluster's own network: a webhook pointed at an internal service would
// otherwise turn seekout into a proxy into it.
//
// The check runs when a connection is dialed, against the IP actually
// connected to, so redirects and DNS that changes after the URL was saved
// are covered too. SEEKOUT_ALLOW_PRIVATE_TARGETS=1 turns it off for local
// development against a local ntfy or webhook receiver.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"syscall"
	"time"
)

// ErrInternal is wrapped by every refusal.
var ErrInternal = errors.New("private or internal address")

// AllowPrivate disables the guard. Read from the environment once at start;
// tests in this package flip it directly.
var AllowPrivate = os.Getenv("SEEKOUT_ALLOW_PRIVATE_TARGETS") == "1"

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, incl. the tailnet
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can map to anything
}

// Internal reports whether ip is not a public unicast address.
func Internal(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || !ip.IsValid() {
		return true
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func control(_, address string, _ syscall.RawConn) error {
	if AllowPrivate {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("netguard: %w", err)
	}
	if Internal(ap.Addr()) {
		return fmt.Errorf("refusing to connect to %s: %w", ap.Addr(), ErrInternal)
	}
	return nil
}

// Client is an HTTP client whose every connection, including redirects,
// goes through the guard. Proxies are not used: a proxy would make the
// dialed address the proxy's, not the target's.
var Client = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: control}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        20,
		ForceAttemptHTTP2:   true,
	},
}

// CheckURL is the save-time version of the guard, for a clear error before
// a bad target is stored: it rejects a URL whose host is, or resolves only
// to, internal addresses. The dial-time check stays the real defence.
func CheckURL(ctx context.Context, raw string) error {
	if AllowPrivate || raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if Internal(ip) {
			return fmt.Errorf("%s is a %w", host, ErrInternal)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil // unresolvable now; the dial-time check decides later
	}
	for _, a := range addrs {
		if !Internal(a) {
			return nil
		}
	}
	return fmt.Errorf("%s resolves to a %w", host, ErrInternal)
}
