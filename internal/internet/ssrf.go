// Package internet gives Socrates two ways to read the web: a search and a
// fetch. Both are optional, both are configured in the admin dashboard, and
// the local fetch is deliberately paranoid about where it is pointed - the
// model chooses the URL, so "http://169.254.169.254/latest/meta-data/" is a
// URL it can choose.
package internet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Version is stamped by the binary so the User-Agent says which Socrates is
// knocking. Sites that block unknown agents can then allow this one by name.
var Version = "dev"

func userAgent() string {
	return "SocratesAgent/" + Version + " (+https://github.com/saschazesiger/SocratesAgent)"
}

// maxRedirects is how many hops a fetch may follow. Each one is checked again,
// because a redirect is the classic way to smuggle a public looking URL into a
// request against localhost.
const maxRedirects = 5

// blockedCIDRs are the ranges a fetch must never reach: this machine, the
// private networks it sits on, and the link local address that cloud providers
// serve instance credentials from.
var blockedCIDRs = mustCIDRs(
	"0.0.0.0/8",          // "this network"
	"10.0.0.0/8",         // RFC1918
	"100.64.0.0/10",      // CGNAT
	"127.0.0.0/8",        // loopback
	"169.254.0.0/16",     // link local, including the 169.254.169.254 metadata service
	"172.16.0.0/12",      // RFC1918
	"192.0.0.0/24",       // IETF protocol assignments
	"192.0.2.0/24",       // TEST-NET-1
	"192.168.0.0/16",     // RFC1918
	"198.18.0.0/15",      // benchmarking
	"198.51.100.0/24",    // TEST-NET-2
	"203.0.113.0/24",     // TEST-NET-3
	"224.0.0.0/4",        // multicast
	"240.0.0.0/4",        // reserved
	"255.255.255.255/32", // broadcast
	"::/128",             // unspecified
	"::1/128",            // loopback
	"fc00::/7",           // unique local
	"fe80::/10",          // link local
	"ff00::/8",           // multicast
	"2001:db8::/32",      // documentation
)

func mustCIDRs(list ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(list))
	for _, entry := range list {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			panic("internet: bad CIDR " + entry)
		}
		out = append(out, network)
	}
	return out
}

// blockedIP reports whether an address is one a fetch must not reach.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// An IPv4 address wrapped as ::ffff:127.0.0.1 is the same address, so it
	// is unwrapped before the ranges are consulted.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, network := range blockedCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedError is what a caller sees when the guard refused an address. It is
// worded for the model, which is the one that picked the URL.
type blockedError struct {
	host   string
	reason string
}

func (e *blockedError) Error() string {
	return fmt.Sprintf("refusing to fetch %s: %s. This tool only reads the public internet; "+
		"use the shell for anything on this machine or on the local network.", e.host, e.reason)
}

// Guard is an HTTP client that will only talk to public addresses. It resolves
// every hostname itself and dials the address it checked, so a name that
// answers differently on the second lookup cannot slip past.
type Guard struct {
	// Resolve is swapped out in tests. Nil means the system resolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
	// Timeout is the whole request budget, redirects included.
	Timeout time.Duration
	// allowHosts is how the package's own tests point a guard at an httptest
	// server on 127.0.0.1. It is keyed by the full host:port, so exempting one
	// test server does not exempt loopback as a whole and every other hop - a
	// redirect above all - still goes through the real check. Nothing outside
	// this package can set it.
	allowHosts map[string]bool
}

// allowed reports whether an authority was explicitly exempted for a test.
func (g *Guard) allowed(authority string) bool { return g.allowHosts[authority] }

func (g *Guard) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if g.Resolve != nil {
		return g.Resolve(ctx, host)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// CheckURL rejects a URL before a single packet leaves: a scheme that is not
// http(s), a missing host, or a name that resolves somewhere private.
func (g *Guard) CheckURL(ctx context.Context, u *url.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return &blockedError{host: u.Redacted(), reason: "only http and https URLs can be fetched"}
	}
	host := u.Hostname()
	if host == "" {
		return &blockedError{host: u.Redacted(), reason: "the URL has no host"}
	}
	if g.allowed(u.Host) {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return &blockedError{host: host, reason: "that is a loopback, private or link local address"}
		}
		return nil
	}
	ips, err := g.lookup(ctx, host)
	if err != nil {
		return fmt.Errorf("could not resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return &blockedError{host: host, reason: "the name resolves to no address at all"}
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return &blockedError{host: host,
				reason: fmt.Sprintf("the name resolves to %s, which is a loopback, private or link local address", ip)}
		}
	}
	return nil
}

// DialContext resolves, checks and only then connects. It is the second half
// of the defence: CheckURL catches the obvious cases with a readable message,
// this catches every connection the HTTP client makes, redirects included.
func (g *Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if g.allowed(address) {
		return dialer.DialContext(ctx, network, address)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else if ips, err = g.lookup(ctx, host); err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		if blockedIP(ip) {
			// One bad answer condemns the name: a resolver that returns a
			// public and a private address is exactly the rebinding trick
			// this is here to stop.
			return nil, &blockedError{host: host,
				reason: fmt.Sprintf("the name resolves to %s, which is a loopback, private or link local address", ip)}
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = &blockedError{host: host, reason: "the name resolves to no usable address"}
	}
	return nil, lastErr
}

// Client is an http.Client that can only reach the public internet.
func (g *Guard) Client() *http.Client {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	transport := &http.Transport{
		DialContext:           g.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		// A proxy would move the connection out from under the guard, and
		// would resolve the name itself. There is none here on purpose.
		Proxy: nil,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("gave up after %d redirects", maxRedirects)
			}
			return g.CheckURL(req.Context(), req.URL)
		},
	}
}
