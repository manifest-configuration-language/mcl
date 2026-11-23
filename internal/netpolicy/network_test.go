package netpolicy

import (
	"net/netip"
	"testing"
)

func TestConfiguredPrivateNetworks(t *testing.T) {
	t.Parallel()

	prefixes, err := configuredPrivateNetworks(" 10.96.0.123/32, 192.168.4.0/24 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 2 {
		t.Fatalf("configured prefix count = %d, want 2", len(prefixes))
	}
	for _, invalid := range []string{"0.0.0.0/0", "127.0.0.1/32", "169.254.0.0/16", "2001:db8::/32", "not-a-prefix"} {
		t.Run(invalid, func(t *testing.T) {
			t.Parallel()
			if _, err := configuredPrivateNetworks(invalid); err == nil {
				t.Fatalf("configuredPrivateNetworks(%q) succeeded", invalid)
			}
		})
	}
}

func TestAllowedExternalIPRequiresExplicitPrivateNetwork(t *testing.T) {
	t.Parallel()

	privateIP := netip.MustParseAddr("10.96.0.123")
	allowed, err := configuredPrivateNetworks("10.96.0.123/32")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		address  string
		allow    []netip.Prefix
		expected bool
	}{
		{name: "public", address: "8.8.8.8", expected: true},
		{name: "private denied by default", address: privateIP.String()},
		{name: "allowlisted private address", address: privateIP.String(), allow: allowed, expected: true},
		{name: "different private address", address: "10.96.0.124", allow: allowed},
		{name: "loopback remains denied", address: "127.0.0.1", allow: allowed},
		{name: "link local remains denied", address: "169.254.169.254", allow: allowed},
		{name: "IPv6 ULA remains denied", address: "fd00::1", allow: allowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if actual := allowedExternalIP(netip.MustParseAddr(test.address), test.allow); actual != test.expected {
				t.Fatalf("allowedExternalIP(%s) = %t, want %t", test.address, actual, test.expected)
			}
		})
	}
}
