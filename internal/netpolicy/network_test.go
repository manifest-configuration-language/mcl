package netpolicy

import (
	"net/netip"
	"testing"
)

func TestConfiguredPrivateNetworks(t *testing.T) {
	t.Parallel()

	destinations, err := configuredPrivateDestinations(" 10.96.0.123/32:8443, 192.168.4.0/24:443 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 2 {
		t.Fatalf("configured destination count = %d, want 2", len(destinations))
	}
	for _, invalid := range []string{"0.0.0.0/0:443", "127.0.0.0/8:443", "169.254.0.0/16:443", "2001:db8::/32:443", "10.0.0.0/8:0", "not-a-prefix"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			if _, err := configuredPrivateDestinations(invalid); err == nil {
				t.Fatalf("configuredPrivateDestinations(%q) succeeded", invalid)
			}
		})
	}
}

func TestAllowedExternalIPRequiresExplicitPrivateNetwork(t *testing.T) {
	t.Parallel()

	privateIP := netip.MustParseAddr("10.96.0.123")
	allowed, err := configuredPrivateDestinations("10.96.0.123/32:8443")
	if err != nil {
		t.Fatal(err)
	}
	loopback, err := configuredPrivateDestinations("127.0.0.1/32:8443")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		address  string
		port     int
		allow    []allowedPrivateDestination
		expected bool
	}{
		{name: "public", address: "8.8.8.8", port: 443, expected: true},
		{name: "private denied by default", address: privateIP.String()},
		{name: "allowlisted private address and port", address: privateIP.String(), port: 8443, allow: allowed, expected: true},
		{name: "different private address", address: "10.96.0.124", port: 8443, allow: allowed},
		{name: "different port", address: privateIP.String(), port: 443, allow: allowed},
		{name: "loopback denied by default", address: "127.0.0.1", port: 8443},
		{name: "explicit loopback address and port", address: "127.0.0.1", port: 8443, allow: loopback, expected: true},
		{name: "different loopback port", address: "127.0.0.1", port: 443, allow: loopback},
		{name: "link local remains denied", address: "169.254.169.254", port: 8443, allow: allowed},
		{name: "IPv6 ULA remains denied", address: "fd00::1", port: 8443, allow: allowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if actual := allowedExternalIP(netip.MustParseAddr(test.address), test.port, test.allow); actual != test.expected {
				t.Fatalf("allowedExternalIP(%s) = %t, want %t", test.address, actual, test.expected)
			}
		})
	}
}
