package forti

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestConfigureSequenceHitsTheEndpointsInOrder pins the order the gateway
// expects. Skipping any of these leaves the session incomplete and the gateway
// closes the tunnel request without a response, which surfaces far away as an
// unexplained EOF.
func TestConfigureSequenceHitsTheEndpointsInOrder(t *testing.T) {
	var visited []string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visited = append(visited, r.URL.Path)

		if r.URL.Path == "/remote/fortisslvpn_xml" {
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><sslvpn-tunnel><ipv4>` +
				`<assigned-addr ipv4="10.110.248.16"/>` +
				`<dns ip="10.140.9.4"/>` +
				`<split-tunnel-include ip="10.0.0.0" mask="255.0.0.0"/>` +
				`</ipv4></sslvpn-tunnel>`))
			return
		}

		// The portal pages answer 403 to a non-browser client even when the
		// allocation succeeds, which must not stop the sequence.
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	gw := gatewayFor(t, server)

	if err := RequestVPNAllocation(context.Background(), gw, "SVPNCOOKIE=x"); err != nil {
		t.Fatalf("RequestVPNAllocation: %v", err)
	}

	config, err := GetConfig(context.Background(), gw, "SVPNCOOKIE=x")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	want := []string{"/remote/index", "/remote/fortisslvpn", "/remote/fortisslvpn_xml"}
	if len(visited) != len(want) {
		t.Fatalf("visited %v, want %v", visited, want)
	}
	for i, path := range want {
		if visited[i] != path {
			t.Errorf("request %d was %s, want %s", i+1, visited[i], path)
		}
	}

	if config.AssignedIPv4 != "10.110.248.16" {
		t.Errorf("assigned address = %q", config.AssignedIPv4)
	}
	if len(config.SplitInclude) != 1 || config.SplitInclude[0].String() != "10.0.0.0/8" {
		t.Errorf("split routes = %v, want [10.0.0.0/8]", config.SplitInclude)
	}
}

// TestRequestVPNAllocationToleratesForbidden covers the behaviour that cost a
// debugging round: the portal answers 403 and openfortivpn ignores it.
func TestRequestVPNAllocationToleratesForbidden(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	if err := RequestVPNAllocation(context.Background(), gatewayFor(t, server), "SVPNCOOKIE=x"); err != nil {
		t.Fatalf("a 403 from the portal was treated as a failure: %v", err)
	}
}

func TestParseNetwork(t *testing.T) {
	network, err := parseNetwork("10.0.0.0", "255.0.0.0")
	if err != nil {
		t.Fatalf("parseNetwork: %v", err)
	}
	if got := network.String(); got != "10.0.0.0/8" {
		t.Errorf("network = %s, want 10.0.0.0/8", got)
	}

	// A host address outside its own mask must be normalised to the network,
	// or `ip route add` refuses it.
	network, err = parseNetwork("10.1.2.3", "255.255.0.0")
	if err != nil {
		t.Fatalf("parseNetwork: %v", err)
	}
	if got := network.String(); got != "10.1.0.0/16" {
		t.Errorf("network = %s, want 10.1.0.0/16", got)
	}

	if _, err := parseNetwork("not-an-ip", "255.0.0.0"); err == nil {
		t.Error("expected an error for a bad address")
	}
	if _, err := parseNetwork("10.0.0.0", "nonsense"); err == nil {
		t.Error("expected an error for a bad mask")
	}
}

func gatewayFor(t *testing.T, server *httptest.Server) Gateway {
	t.Helper()

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting the server address: %v", err)
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parsing the port: %v", err)
	}

	return Gateway{Host: host, Port: portNumber, Insecure: true}
}

// TestGetConfigParsesARealGatewayDocument works from a capture of what a live
// FortiGate returns. The shape matters: this gateway publishes its networks
// under <split-tunnel-info><addr>, and reading only <split-tunnel-include>
// silently yields no routes at all — which looks exactly like a gateway that
// wants a full tunnel.
func TestGetConfigParsesARealGatewayDocument(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("testdata", "sslvpn_tunnel.xml"))
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = w.Write(document)
	}))
	defer server.Close()

	config, err := GetConfig(context.Background(), gatewayFor(t, server), "SVPNCOOKIE=x")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	if config.AssignedIPv4 != "10.110.248.72" {
		t.Errorf("assigned address = %q, want 10.110.248.72", config.AssignedIPv4)
	}
	if len(config.DNSServers) != 2 {
		t.Errorf("DNS servers = %v, want two", config.DNSServers)
	}

	want := []string{
		"10.0.0.0/8",
		"192.168.100.0/24",
		"172.17.29.16/28",
		"172.17.0.0/16",
		"15.161.144.185/32",
		"192.168.146.0/23",
	}

	got := make([]string, 0, len(config.SplitInclude))
	for _, route := range config.SplitInclude {
		got = append(got, route.String())
	}

	if len(got) != len(want) {
		t.Fatalf("got %d routes %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("route %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestGetConfigDropsRepeatedRoutes covers the duplicates a real gateway sends:
// the kernel rejects a route that already exists, so a repeat would surface as
// a failure while installing.
func TestGetConfigDropsRepeatedRoutes(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("testdata", "sslvpn_tunnel.xml"))
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}

	if occurrences := strings.Count(string(document), "15.161.144.185"); occurrences < 2 {
		t.Fatalf("the capture holds %d copies of the repeated address, expected the duplicate", occurrences)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(document)
	}))
	defer server.Close()

	config, err := GetConfig(context.Background(), gatewayFor(t, server), "SVPNCOOKIE=x")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	seen := map[string]int{}
	for _, route := range config.SplitInclude {
		seen[route.String()]++
	}
	for route, count := range seen {
		if count > 1 {
			t.Errorf("route %s appears %d times, want once", route, count)
		}
	}
}
