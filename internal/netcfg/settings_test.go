package netcfg

import (
	"net"
	"testing"
)

// mustCIDR is a helper for building the route lists the tests compare against.
func mustCIDR(t *testing.T, cidr string) net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("parsing %s: %v", cidr, err)
	}

	return *network
}

func TestHalfInternetRoutesCoverEverythingWithoutTheDefaultRoute(t *testing.T) {
	// The pair is used instead of 0.0.0.0/0 so the system's own default route
	// is never replaced: nothing has to be saved and restored, and a daemon
	// that dies leaves the original path in place.
	if len(halfInternetRoutes) != 2 {
		t.Fatalf("got %d routes, want 2", len(halfInternetRoutes))
	}

	for _, cidr := range halfInternetRoutes {
		network := mustCIDR(t, cidr)
		ones, bits := network.Mask.Size()
		if ones != 1 || bits != 32 {
			t.Errorf("%s is a /%d, want a /1 so it outranks the default route", cidr, ones)
		}
	}

	// Together they must cover both halves of the address space.
	low := mustCIDR(t, halfInternetRoutes[0])
	high := mustCIDR(t, halfInternetRoutes[1])

	if !low.Contains(net.IPv4(1, 1, 1, 1)) {
		t.Errorf("%s does not cover the lower half", low.String())
	}
	if !high.Contains(net.IPv4(200, 0, 0, 1)) {
		t.Errorf("%s does not cover the upper half", high.String())
	}
}

func TestParseRouteGet(t *testing.T) {
	tests := map[string]struct {
		output  string
		wantVia string
		wantDev string
	}{
		"via a gateway": {
			output:  "185.243.193.130 via 192.168.1.1 dev wlp0s20f3 src 192.168.1.78 uid 1000 \n    cache \n",
			wantVia: "192.168.1.1",
			wantDev: "wlp0s20f3",
		},
		"directly attached": {
			output:  "10.110.248.1 dev svpn0 src 10.110.248.31 uid 1000 \n    cache \n",
			wantVia: "",
			wantDev: "svpn0",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			via, dev := parseRouteGet(test.output)
			if via != test.wantVia {
				t.Errorf("via = %q, want %q", via, test.wantVia)
			}
			if dev != test.wantDev {
				t.Errorf("dev = %q, want %q", dev, test.wantDev)
			}
		})
	}
}
