// Package fetch is the SSRF-guarded HTTP client used by offline downloads.
//
// Two layers are checked:
//  1. the URL itself (scheme, shape) in ValidateURL;
//  2. the actual IP the dialer connects to, via net.Dialer.Control, which is
//     the defense that survives DNS rebinding. Redirects are re-validated and
//     capped.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

const (
	// MaxURLLength bounds stored source URLs.
	MaxURLLength = 2048
	// MaxRedirects caps the redirect chain.
	MaxRedirects = 5

	dialTimeout           = 15 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
)

// ErrInvalidURL marks a syntactically unacceptable source URL (400).
var ErrInvalidURL = errors.New("fetch: invalid url")

// ErrBlocked marks an address the guard refuses to connect to (400).
var ErrBlocked = errors.New("fetch: target address is not allowed")

// ErrTooManyRedirects marks an over-long redirect chain.
var ErrTooManyRedirects = errors.New("fetch: too many redirects")

// blockedNets are rejected even though some are global unicast per net.IP.
var blockedNets = mustCIDRs(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32",
)

func mustCIDRs(raw ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(raw))
	for _, s := range raw {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			panic("fetch: bad built-in CIDR " + s)
		}
		out = append(out, n)
	}
	return out
}

// Client wraps an http.Client with the SSRF policy applied.
type Client struct {
	hc           *http.Client
	allowPrivate bool
}

// New builds a client. allowPrivate disables the private/loopback guard and
// must only be used for trusted LAN sources (flag -allow-private-downloads).
func New(allowPrivate bool) *Client {
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	dialer.Control = func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBlocked, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: unresolved dial address", ErrBlocked)
		}
		return checkIP(ip, allowPrivate)
	}
	tr := &http.Transport{
		Proxy:                 nil, // never inherit an ambient proxy for server-side fetches
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	hc := &http.Client{Transport: tr}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return ErrTooManyRedirects
		}
		if _, err := ValidateURL(req.URL.String()); err != nil {
			return err
		}
		if !allowPrivate {
			if err := checkHost(req.Context(), req.URL.Hostname()); err != nil {
				return err
			}
		}
		return nil
	}
	return &Client{hc: hc, allowPrivate: allowPrivate}
}

// ValidateURL accepts only absolute http/https URLs without credentials.
func ValidateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLLength {
		return nil, fmt.Errorf("%w: length", ErrInvalidURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrInvalidURL)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials are not allowed", ErrInvalidURL)
	}
	if u.Port() != "" {
		if p, err := net.LookupPort("tcp", u.Port()); err != nil || p <= 0 {
			return nil, fmt.Errorf("%w: bad port", ErrInvalidURL)
		}
	}
	return u, nil
}

// CheckURL validates raw as a fetchable public target without fetching it. It
// resolves the host and rejects private/loopback addresses so callers can fail
// before creating a job row; Open still re-checks the address it dials.
func (c *Client) CheckURL(ctx context.Context, raw string) error {
	u, err := ValidateURL(raw)
	if err != nil {
		return err
	}
	if c.allowPrivate {
		return nil
	}
	return checkHost(ctx, u.Hostname())
}

// Open validates the URL and issues a GET request.
func (c *Client) Open(ctx context.Context, raw string) (*http.Response, error) {
	u, err := ValidateURL(raw)
	if err != nil {
		return nil, err
	}
	if !c.allowPrivate {
		if err := checkHost(ctx, u.Hostname()); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", "rapig offline-download")
	req.Header.Set("Accept", "*/*")
	return c.hc.Do(req)
}

// checkHost resolves the host and rejects it when any address is blocked.
// This makes obvious cases fail before dialing; the dialer Control hook is the
// authoritative check.
func checkHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip, false)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrBlocked, host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %q has no addresses", ErrBlocked, host)
	}
	for _, a := range addrs {
		if err := checkIP(a.IP, false); err != nil {
			return err
		}
	}
	return nil
}

func checkIP(ip net.IP, allowPrivate bool) error {
	if allowPrivate {
		return nil
	}
	if ip == nil || !ip.IsGlobalUnicast() {
		return fmt.Errorf("%w: non-unicast address %s", ErrBlocked, ip)
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return fmt.Errorf("%w: address %s", ErrBlocked, ip)
		}
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: address %s", ErrBlocked, ip)
	}
	return nil
}
