package forti

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sorintlab/errors"
)

// DefaultUserAgent is the identifier openfortivpn presents. Some gateways
// refuse clients they do not recognise, so the default matches a known-good
// one rather than naming this program.
const DefaultUserAgent = "Mozilla/5.0 SV1"

// maxConfigSize bounds the tunnel configuration read from the gateway.
const maxConfigSize = 1 << 20

// TunnelConfig is the subset of the gateway's XML configuration worth
// reporting. Fields the gateway omits stay empty rather than failing the
// request: the shape varies between FortiOS versions, and this is descriptive
// output, not something the tunnel depends on.
type TunnelConfig struct {
	AssignedIPv4 string
	DNSServers   []string
	DNSSuffix    string
	// SplitInclude are the destinations the gateway says belong to the VPN.
	// An empty list means it did not ask for split tunnelling.
	SplitInclude []net.IPNet
	Raw          []byte
}

// splitAddrXML is one entry of the split-tunnel list.
type splitAddrXML struct {
	IP   string `xml:"ip,attr"`
	Mask string `xml:"mask,attr"`
}

// sslvpnTunnelXML mirrors the parts of /remote/fortisslvpn_xml that are read.
type sslvpnTunnelXML struct {
	IPv4 struct {
		AssignedAddr struct {
			IPv4 string `xml:"ipv4,attr"`
		} `xml:"assigned-addr"`
		DNS []struct {
			IP     string `xml:"ip,attr"`
			Domain string `xml:"domain,attr"`
		} `xml:"dns"`
		// FortiOS publishes the split-tunnel list under either name depending
		// on the version, so both are read.
		SplitInclude []splitAddrXML `xml:"split-tunnel-include"`
		SplitInfo    struct {
			Addr []splitAddrXML `xml:"addr"`
		} `xml:"split-tunnel-info"`
	} `xml:"ipv4"`
}

// parseNetwork turns the gateway's dotted address and mask into a network.
func parseNetwork(address, mask string) (net.IPNet, error) {
	ip := net.ParseIP(strings.TrimSpace(address)).To4()
	if ip == nil {
		return net.IPNet{}, errors.Errorf("%q is not an IPv4 address", address)
	}

	maskIP := net.ParseIP(strings.TrimSpace(mask)).To4()
	if maskIP == nil {
		return net.IPNet{}, errors.Errorf("%q is not an IPv4 mask", mask)
	}

	network := net.IPNet{IP: ip.Mask(net.IPv4Mask(maskIP[0], maskIP[1], maskIP[2], maskIP[3])),
		Mask: net.IPv4Mask(maskIP[0], maskIP[1], maskIP[2], maskIP[3])}

	return network, nil
}

// httpClient returns a client for the gateway's ordinary HTTP endpoints.
//
// Keep-alives are disabled so every step runs on a fresh TLS connection, the
// way openfortivpn reconnects between phases.
func (g Gateway) httpClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   g.tlsConfig(),
			DisableKeepAlives: true,
			// The gateway negotiates no ALPN protocol, and openfortivpn speaks
			// plain HTTP/1.1. Pin it rather than leaving the choice to Go.
			ForceAttemptHTTP2: false,
			TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// get performs one authenticated request against the gateway, sending the same
// headers openfortivpn does.
func (g Gateway) get(ctx context.Context, client *http.Client, path, cookie string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL()+path, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "building the request for %s", path)
	}

	request.Header.Set("User-Agent", g.userAgent())
	request.Header.Set("Accept", "*/*")
	// Set explicitly so Go does not offer gzip and transparently decode it;
	// openfortivpn asks for an unencoded body.
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Pragma", "no-cache")
	request.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate")
	request.Header.Set("If-Modified-Since", "Sat, 1 Jan 2000 00:00:00 GMT")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Cookie", cookie)

	// openfortivpn sends "Host: <host>:<port>"; Go would drop the default
	// port. Set it explicitly so the gateway sees the same value.
	request.Host = g.Addr()

	g.debugf("GET %s", path)
	g.debugf("  Host: %s", request.Host)
	g.debugf("  Cookie: %s", cookie)

	response, err := client.Do(request)
	if err != nil {
		return nil, errors.Wrapf(err, "requesting %s", path)
	}

	g.debugf("  -> HTTP %d (%s)", response.StatusCode, response.Proto)
	for name, values := range response.Header {
		for _, value := range values {
			g.debugf("  -> %s: %s", name, value)
		}
	}

	return response, nil
}

func (g Gateway) userAgent() string {
	if g.UserAgent != "" {
		return g.UserAgent
	}
	return DefaultUserAgent
}

// RequestVPNAllocation asks the gateway to allocate a VPN session for the
// authenticated cookie.
//
// This step is what makes the gateway willing to serve the tunnel: without it
// /remote/sslvpn-tunnel is closed without a response.
//
// Both endpoints commonly answer 403 to a non-browser client, which is not a
// failure: the allocation is a side effect of the request being processed, so
// only a transport error stops the sequence.
func RequestVPNAllocation(ctx context.Context, gw Gateway, cookie string) error {
	client := gw.httpClient()

	for _, path := range []string{"/remote/index", "/remote/fortisslvpn"} {
		response, err := gw.get(ctx, client, path, cookie)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(io.LimitReader(response.Body, maxConfigSize))
		_ = response.Body.Close()

		gw.debugf("  -> body: %s", summarise(body, 2000))

		// The status is deliberately not checked. What matters is that the
		// gateway processed the request and allocated the session; these
		// endpoints are portal pages that answer 403 to a non-browser client
		// even when the allocation succeeds. openfortivpn ignores the status
		// here too, treating only a transport failure as fatal.
	}

	return nil
}

// summarise renders a response body as a single readable line, collapsing
// whitespace and stripping HTML tags so a FortiGate error page reduces to its
// message.
func summarise(body []byte, limit int) string {
	text := string(body)

	// Drop tags rather than parsing: the goal is a legible error, not fidelity.
	var out strings.Builder
	depth := 0
	for _, r := range text {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			out.WriteRune(r)
		}
	}

	fields := strings.Fields(out.String())
	joined := strings.Join(fields, " ")

	if joined == "" {
		return fmt.Sprintf("(%d bytes, no text)", len(body))
	}
	if len(joined) > limit {
		return joined[:limit] + "…"
	}

	return joined
}

// GetConfig fetches the tunnel configuration the gateway assigned.
func GetConfig(ctx context.Context, gw Gateway, cookie string) (TunnelConfig, error) {
	client := gw.httpClient()

	response, err := gw.get(ctx, client, "/remote/fortisslvpn_xml", cookie)
	if err != nil {
		return TunnelConfig{}, err
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxConfigSize))
	if err != nil {
		return TunnelConfig{}, errors.Wrapf(err, "reading the tunnel configuration")
	}

	if response.StatusCode != http.StatusOK {
		return TunnelConfig{}, errors.Errorf("gateway answered HTTP %d for the tunnel configuration: %s",
			response.StatusCode, summarise(body, 400))
	}

	config := TunnelConfig{Raw: body}

	var parsed sslvpnTunnelXML
	if err := xml.Unmarshal(body, &parsed); err != nil {
		// The XML shape varies between FortiOS versions. A configuration that
		// cannot be parsed is worth reporting, but it does not stop the
		// tunnel, so return what was read and let the caller decide.
		return config, errors.Wrapf(err, "parsing the tunnel configuration")
	}

	config.AssignedIPv4 = parsed.IPv4.AssignedAddr.IPv4
	for _, dns := range parsed.IPv4.DNS {
		if dns.IP != "" {
			config.DNSServers = append(config.DNSServers, dns.IP)
		}
		if dns.Domain != "" && config.DNSSuffix == "" {
			config.DNSSuffix = dns.Domain
		}
	}
	// The list runs to a couple of hundred entries and repeats some of them.
	// A repeat would be rejected by the kernel as an existing route, so they
	// are collapsed here rather than being handled at every use.
	seen := make(map[string]bool)
	for _, route := range append(parsed.IPv4.SplitInclude, parsed.IPv4.SplitInfo.Addr...) {
		network, err := parseNetwork(route.IP, route.Mask)
		if err != nil {
			// One unusable route should not cost the whole configuration, so
			// note it and keep the rest.
			gw.debugf("skipping split route %s/%s: %v", route.IP, route.Mask, err)
			continue
		}

		key := network.String()
		if seen[key] {
			continue
		}
		seen[key] = true

		config.SplitInclude = append(config.SplitInclude, network)
	}

	return config, nil
}
