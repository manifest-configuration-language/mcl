package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

var blockedNetworkPrefixes = mustParsePrefixes([]string{
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"5f00::/16",
	"3fff::/20",
})

var globalIPv6Prefix = netip.MustParsePrefix("2000::/3")

func mustParsePrefixes(prefixes []string) []netip.Prefix {
	parsed := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		parsed = append(parsed, netip.MustParsePrefix(prefix))
	}

	return parsed
}

func allowedExternalIP(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is6() && !globalIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range blockedNetworkPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}

	return true
}

func externalDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse external dependency address: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid external dependency port %q", port)
	}

	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve external dependency host %q: %w", host, err)
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("external dependency host %q has no IP addresses", host)
	}
	for _, resolved := range addresses {
		if !allowedExternalIP(resolved) {
			return nil, fmt.Errorf("external dependency host %q resolves to a restricted IP address %s", host, resolved)
		}
	}

	dialer := net.Dialer{Timeout: 30 * time.Second}
	var lastError error
	for _, resolved := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(resolved.String(), port))
		if err == nil {
			return connection, nil
		}
		lastError = err
	}

	return nil, fmt.Errorf("connect to external dependency host %q: %w", host, lastError)
}

// NewExternalHTTPTransportは外部依存取得向けの送信先IP制限付きHTTP transportを作成します。
func NewExternalHTTPTransport() (*http.Transport, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("initialize external dependency transport")
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DialContext = externalDialContext
	transport.DialTLSContext = nil

	return transport, nil
}
