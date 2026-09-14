package vpn

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ppp"
	"github.com/paccolamano/svpn/internal/service/netcfg"
	"github.com/paccolamano/svpn/internal/service/tundev"
)

// fakeLink is a gateway on the other end of the tunnel: it negotiates PPP the
// way a FortiGate does, then goes quiet, which is what a healthy idle link
// looks like.
type fakeLink struct {
	toClient chan []byte
	closed   chan struct{}
	closeOne sync.Once

	mu       sync.Mutex
	deadline time.Time
	sentLCP  bool
	sentIPCP bool
}

func newFakeLink() *fakeLink {
	return &fakeLink{toClient: make(chan []byte, 32), closed: make(chan struct{})}
}

func (l *fakeLink) ReadPacket() ([]byte, error) {
	l.mu.Lock()
	deadline := l.deadline
	l.mu.Unlock()

	var expired <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		expired = timer.C
	}

	select {
	case frame := <-l.toClient:
		return frame, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-expired:
		return nil, context.DeadlineExceeded
	}
}

func (l *fakeLink) queue(packet ppp.Packet) { l.toClient <- packet.Encode() }

func (l *fakeLink) WritePacket(frame []byte) error {
	select {
	case <-l.closed:
		return net.ErrClosed
	default:
	}

	packet, err := ppp.Decode(frame)
	if err != nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	switch packet.Protocol {
	case ppp.ProtocolLCP:
		l.answerLCP(packet)
	case ppp.ProtocolIPCP:
		l.answerIPCP(packet)
	}

	return nil
}

func (l *fakeLink) answerLCP(packet ppp.Packet) {
	if packet.Code != ppp.CodeConfigureRequest {
		return
	}

	l.queue(ppp.Packet{Protocol: ppp.ProtocolLCP, Code: ppp.CodeConfigureAck,
		Identifier: packet.Identifier, Data: packet.Data})

	// The gateway configures its own direction too, or the link never opens.
	if !l.sentLCP {
		l.sentLCP = true
		l.queue(ppp.Packet{Protocol: ppp.ProtocolLCP, Code: ppp.CodeConfigureRequest,
			Identifier: 100, Data: []byte{1, 4, 0x05, 0xd4}})
	}
}

func (l *fakeLink) answerIPCP(packet ppp.Packet) {
	if packet.Code != ppp.CodeConfigureRequest {
		return
	}

	options, err := ppp.DecodeOptions(packet.Data)
	if err != nil {
		return
	}

	// An unspecified address is the client asking to be assigned one, which the
	// gateway answers with a Nak carrying the real values.
	for _, option := range options {
		if option.Type != ppp.IPCPOptionIPAddress {
			continue
		}
		if !isZero(option.Value) {
			continue
		}

		l.queue(ppp.Packet{Protocol: ppp.ProtocolIPCP, Code: ppp.CodeConfigureNak,
			Identifier: packet.Identifier, Data: ppp.EncodeOptions([]ppp.Option{
				{Type: ppp.IPCPOptionIPAddress, Value: []byte{10, 110, 248, 51}},
				{Type: ppp.IPCPOptionPrimaryDNS, Value: []byte{10, 140, 9, 4}},
				{Type: ppp.IPCPOptionSecondDNS, Value: []byte{10, 0, 16, 26}},
			})})

		return
	}

	l.queue(ppp.Packet{Protocol: ppp.ProtocolIPCP, Code: ppp.CodeConfigureAck,
		Identifier: packet.Identifier, Data: packet.Data})

	if !l.sentIPCP {
		l.sentIPCP = true
		l.queue(ppp.Packet{Protocol: ppp.ProtocolIPCP, Code: ppp.CodeConfigureRequest,
			Identifier: 200, Data: ppp.EncodeOptions([]ppp.Option{
				{Type: ppp.IPCPOptionIPAddress, Value: []byte{10, 110, 248, 1}},
			})})
	}
}

func (l *fakeLink) SetReadDeadline(deadline time.Time) error {
	l.mu.Lock()
	l.deadline = deadline
	l.mu.Unlock()

	return nil
}

func (l *fakeLink) Close() error {
	l.closeOne.Do(func() { close(l.closed) })

	return nil
}

func isZero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}

	return true
}

// quietDevice is a tun interface with no traffic on it. Its Read parks until
// the device is closed, which is how the real one behaves: wireguard-go has no
// read deadline, and only Close releases a blocked read.
type quietDevice struct {
	closed   chan struct{}
	closeOne sync.Once
	// reading is closed once a read is actually parked. Tests that need the
	// data plane to be blocked have to wait for it: without that the pump
	// goroutine may still be unscheduled, notice the cancelled context at the
	// top of its loop and exit without ever reading, which hides the deadlock
	// this file exists to catch.
	reading chan struct{}
	readOne sync.Once
}

func newQuietDevice() *quietDevice {
	return &quietDevice{closed: make(chan struct{}), reading: make(chan struct{})}
}

func (d *quietDevice) Name() (string, error) { return "svpn0", nil }
func (d *quietDevice) MTU() (int, error)     { return 1400, nil }

func (d *quietDevice) Read([]byte) (int, error) {
	d.readOne.Do(func() { close(d.reading) })
	<-d.closed

	return 0, net.ErrClosed
}

// waitUntilReading blocks until the data plane is parked in Read.
func (d *quietDevice) waitUntilReading(t *testing.T) {
	t.Helper()

	select {
	case <-d.reading:
	case <-time.After(5 * time.Second):
		t.Fatal("the device pump never reached a read")
	}
}

func (d *quietDevice) Write(packet []byte) (int, error) { return len(packet), nil }

func (d *quietDevice) Close() error {
	d.closeOne.Do(func() { close(d.closed) })

	return nil
}

// isClosed reports whether the teardown reached the interface.
func (d *quietDevice) isClosed() bool {
	select {
	case <-d.closed:
		return true
	default:
		return false
	}
}

// recordingConfigurator stands in for the privileged half, which cannot run
// without CAP_NET_ADMIN.
type recordingConfigurator struct {
	mu       sync.Mutex
	applied  netcfg.Settings
	appliedN int
	reverted int
}

func (c *recordingConfigurator) Apply(settings netcfg.Settings) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.applied = settings
	c.appliedN++

	return nil
}

func (c *recordingConfigurator) Revert() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.reverted++

	return nil
}

func (c *recordingConfigurator) counts() (applied, reverted int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.appliedN, c.reverted
}

// fakeGateway serves the endpoints Connect walks before it opens the tunnel.
func fakeGateway(t *testing.T) forti.Gateway {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote/fortisslvpn_xml" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><sslvpn-tunnel><ipv4>` +
				`<assigned-addr ipv4="10.110.248.51"/>` +
				`<dns ip="10.140.9.4" domain="example.test"/>` +
				`<split-tunnel-include ip="10.0.0.0" mask="255.0.0.0"/>` +
				`</ipv4></sslvpn-tunnel>`))
			return
		}

		// The portal pages answer 403 to a non-browser client; the allocation
		// still happens.
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(server.Close)

	host, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatalf("splitting the test server address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parsing the test server port: %v", err)
	}

	return forti.Gateway{Host: host, Port: port, Insecure: true}
}

func connectToFake(t *testing.T, link Link, device tundev.Device, config netcfg.Configurator) *Connection {
	t.Helper()

	connection, err := Connect(context.Background(), Options{
		Gateway:      fakeGateway(t),
		Cookie:       "SVPNCOOKIE=test",
		Dial:         func(context.Context) (Link, error) { return link, nil },
		NewTUN:       func(string, int) (tundev.Device, error) { return device, nil },
		Configurator: config,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	return connection
}

// TestCloseReturnsOnAQuietInterface covers the deadlock that made svpn down
// hang: Close cannot wait for the data-plane goroutines before tearing the
// device down, because a tun read is only released by closing the device. An
// interface with no traffic on it is the ordinary case, not an edge one.
func TestCloseReturnsOnAQuietInterface(t *testing.T) {
	device := newQuietDevice()
	connection := connectToFake(t, newFakeLink(), device, &recordingConfigurator{})

	// The deadlock only exists once the pump is actually parked in a read, so
	// closing before that would pass against the broken ordering too.
	device.waitUntilReading(t)

	closed := make(chan error, 1)
	go func() { closed <- connection.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: it is waiting for a device read that nothing will release")
	}
}

// TestConnectAppliesWhatTheGatewayAssigned pins the values that reach the
// privileged half, which is the only place they can still be checked without
// CAP_NET_ADMIN.
func TestConnectAppliesWhatTheGatewayAssigned(t *testing.T) {
	config := &recordingConfigurator{}
	connection := connectToFake(t, newFakeLink(), newQuietDevice(), config)
	defer func() { _ = connection.Close() }()

	details := connection.Details()
	if details.Interface != "svpn0" {
		t.Errorf("interface = %q, want svpn0", details.Interface)
	}
	if want := net.IPv4(10, 110, 248, 51); !details.LocalIP.Equal(want) {
		t.Errorf("local address = %s, want %s", details.LocalIP, want)
	}
	if want := net.IPv4(10, 110, 248, 1); !details.PeerIP.Equal(want) {
		t.Errorf("peer address = %s, want %s", details.PeerIP, want)
	}
	if len(details.DNS) != 2 {
		t.Errorf("DNS servers = %v, want two", details.DNS)
	}

	// A nil Routes in Options adopts the gateway's split-tunnel list.
	if len(details.Routes) != 1 || details.Routes[0].String() != "10.0.0.0/8" {
		t.Errorf("routes = %v, want [10.0.0.0/8]", details.Routes)
	}

	config.mu.Lock()
	applied := config.applied
	config.mu.Unlock()

	if applied.Interface != "svpn0" {
		t.Errorf("configured interface = %q", applied.Interface)
	}
	if applied.FullTunnel {
		t.Error("a split tunnel was configured as a full tunnel")
	}
	// The gateway published a suffix and the caller named none, so the
	// gateway's is what gets claimed.
	if len(applied.DNSDomains) != 1 || applied.DNSDomains[0] != "example.test" {
		t.Errorf("DNS domains = %v, want [example.test]", applied.DNSDomains)
	}
	// The gateway must keep reaching the network off the tunnel.
	if applied.GatewayIP == nil {
		t.Error("no gateway address was pinned")
	}
}

// TestCloseRevertsTheConfigurationOnce checks the teardown puts the machine
// back, and that a second Close does not run it again: shutdown paths call it
// from more than one place.
func TestCloseRevertsTheConfigurationOnce(t *testing.T) {
	config := &recordingConfigurator{}
	link := newFakeLink()
	device := newQuietDevice()

	connection := connectToFake(t, link, device, config)

	if err := connection.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	applied, reverted := config.counts()
	if applied != 1 {
		t.Errorf("Apply ran %d times, want 1", applied)
	}
	if reverted != 1 {
		t.Errorf("Revert ran %d times, want 1", reverted)
	}

	if !device.isClosed() {
		t.Error("the interface was left open")
	}

	select {
	case <-connection.Done():
	default:
		t.Error("Done was not closed by Close")
	}

	if err := connection.Err(); err != nil {
		t.Errorf("a clean close reported a failure: %v", err)
	}
}

// TestADroppedLinkSurfacesThroughDone covers the gateway going away on its
// own: the daemon watches Done to notice, and reports Err as the reason.
func TestADroppedLinkSurfacesThroughDone(t *testing.T) {
	link := newFakeLink()
	connection := connectToFake(t, link, newQuietDevice(), &recordingConfigurator{})
	defer func() { _ = connection.Close() }()

	_ = link.Close()

	select {
	case <-connection.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a dropped link did not close Done")
	}

	if connection.Err() == nil {
		t.Error("a dropped link reported no failure")
	}
}
