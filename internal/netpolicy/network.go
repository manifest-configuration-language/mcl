package netpolicy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
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

var privateIPv4Prefixes = mustParsePrefixes([]string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
})

type allowedPrivateDestination struct {
	prefix netip.Prefix
	port   int
}

func mustParsePrefixes(prefixes []string) []netip.Prefix {
	parsed := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		parsed = append(parsed, netip.MustParsePrefix(prefix))
	}

	return parsed
}

func allowedExternalIP(address netip.Addr, port int, allowedPrivateDestinations []allowedPrivateDestination) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if address.IsLoopback() {
		return allowedDestination(allowedPrivateDestinations, address, port)
	}
	if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.IsPrivate() {
		return allowedDestination(allowedPrivateDestinations, address, port)
	}
	if !address.IsGlobalUnicast() {
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

func allowedDestination(destinations []allowedPrivateDestination, address netip.Addr, port int) bool {
	for _, destination := range destinations {
		if destination.port == port && destination.prefix.Contains(address) {
			return true
		}
	}

	return false
}

func externalDialContext(ctx context.Context, network, address string, allowedPrivateDestinations []allowedPrivateDestination) (net.Conn, error) {
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
		if !allowedExternalIP(resolved, portNumber, allowedPrivateDestinations) {
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
// MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONSは、接続を許可するRFC 1918 CIDRとPortのカンマ区切り一覧です。
// MCL_EXTERNAL_CA_FILEは、システムCAへ追加するPEM証明書ファイルです。
func NewExternalHTTPTransport() (*http.Transport, error) {
	allowedPrivateDestinations, err := configuredPrivateDestinations(os.Getenv("MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONS"))
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("initialize external dependency transport")
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return externalDialContext(ctx, network, address, allowedPrivateDestinations)
	}
	transport.DialTLSContext = nil
	if caFile := strings.TrimSpace(os.Getenv("MCL_EXTERNAL_CA_FILE")); caFile != "" {
		// #nosec G703 -- The CA file path is provided by the trusted MCL operator.
		certificates, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read external dependency CA file: %w", err)
		}
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system CA certificates: %w", err)
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		if !rootCAs.AppendCertsFromPEM(certificates) {
			return nil, errors.New("external dependency CA file contains no certificates")
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = new(tls.Config)
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.RootCAs = rootCAs
	}

	return transport, nil
}

func configuredPrivateDestinations(value string) ([]allowedPrivateDestination, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	var destinations []allowedPrivateDestination
	for raw := range strings.SplitSeq(value, ",") {
		network, portString, found := strings.CutLast(strings.TrimSpace(raw), ":")
		if !found {
			return nil, fmt.Errorf("MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONS entry %q must use CIDR:port format", raw)
		}
		port, err := strconv.Atoi(portString)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONS entry %q has an invalid port", raw)
		}
		prefix, err := netip.ParsePrefix(network)
		if err != nil {
			return nil, fmt.Errorf("parse MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONS CIDR: %w", err)
		}
		prefix = prefix.Masked()
		allowed := prefix.Bits() == prefix.Addr().BitLen() && prefix.Addr().IsLoopback()
		for _, privatePrefix := range privateIPv4Prefixes {
			if !allowed && privatePrefix.Contains(prefix.Addr()) && prefix.Bits() >= privatePrefix.Bits() {
				allowed = true

				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("MCL_DANGEROUS_ALLOWED_PRIVATE_DESTINATIONS CIDR %q must be within an RFC 1918 network or an exact loopback address", network)
		}
		destinations = append(destinations, allowedPrivateDestination{prefix: prefix, port: port})
	}

	return destinations, nil
}
