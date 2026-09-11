// Package vpn brings a FortiGate SSL VPN link all the way up: authenticated
// tunnel, PPP negotiation, virtual interface and network configuration.
package vpn

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/netcfg"
	"github.com/paccolamano/svpn/internal/ppp"
	"github.com/paccolamano/svpn/internal/tundev"
)

// defaultMTU leaves room for the TLS record, the gateway's 6-byte frame header
// and the PPP header on top of a 1500-byte path.
const defaultMTU = 1400

// readTimeout bounds a single read from the tunnel. It has to outlast the
// gateway's keepalive interval, or a quiet but healthy link would be torn down.
const readTimeout = 120 * time.Second

// Link carries PPP frames and can be closed.
type Link interface {
	ppp.Transport
	SetReadDeadline(time.Time) error
	Close() error
}

// Options configures a connection.
//
// The three function fields are seams: creating a TUN device and changing
// routes need privileges the tests do not have, so they are replaced with
// fakes there. Leaving them nil selects the real implementations.
type Options struct {
	Gateway forti.Gateway
	Cookie  string
	// InterfaceName is a hint; the operating system may choose another.
	InterfaceName string
	MTU           int
	// Routes are sent through the tunnel. A nil value adopts the split-tunnel
	// list the gateway publishes; a non-nil empty list installs none.
	Routes []net.IPNet
	// FullTunnel sends everything through the tunnel when neither Routes nor
	// the gateway supply a list.
	//
	// It is off by default because a gateway that publishes no split routes
	// often does not route to the internet either, and capturing everything
	// then costs the user every site outside the company.
	FullTunnel bool
	// DNSDomains are the domains resolved through the tunnel's servers. Empty
	// with a split tunnel leaves resolution alone; with a full tunnel every
	// domain is claimed regardless.
	DNSDomains []string
	Logf       func(format string, args ...any)

	Dial         func(ctx context.Context) (Link, error)
	NewTUN       func(name string, mtu int) (tundev.Device, error)
	Configurator netcfg.Configurator
}

// Details describes an established connection.
type Details struct {
	Interface string
	LocalIP   net.IP
	PeerIP    net.IP
	DNS       []net.IP
	Routes    []net.IPNet
}

// Connection is a live VPN session.
type Connection struct {
	details Details
	link    Link
	device  tundev.Device
	config  netcfg.Configurator
	session *ppp.Session
	logf    func(format string, args ...any)

	bytesIn  atomic.Int64
	bytesOut atomic.Int64

	cancel context.CancelFunc
	wg     sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
	// done is closed when a data-plane goroutine stops, so callers can notice
	// a link that dropped on its own instead of polling.
	done     chan struct{}
	doneOnce sync.Once
	failure  atomic.Pointer[error]
}

// Connect establishes the tunnel, negotiates PPP and configures the system.
//
// On any failure everything already created is torn down before returning, so
// a caller never has to clean up after an error.
func Connect(ctx context.Context, options Options) (*Connection, error) {
	logf := options.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	mtu := options.MTU
	if mtu <= 0 {
		mtu = defaultMTU
	}

	dial := options.Dial
	if dial == nil {
		dial = func(ctx context.Context) (Link, error) {
			tunnel, _, err := forti.OpenTunnel(ctx, options.Gateway, options.Cookie, nil)

			return tunnel, errors.Wrapf(err, "opening the tunnel")
		}
	}

	newTUN := options.NewTUN
	if newTUN == nil {
		newTUN = tundev.Create
	}

	configurator := options.Configurator
	if configurator == nil {
		configurator = netcfg.New()
	}

	logf("requesting a VPN allocation")
	if err := forti.RequestVPNAllocation(ctx, options.Gateway, options.Cookie); err != nil {
		return nil, errors.Wrapf(err, "allocating the VPN")
	}

	// Fetching the configuration is part of the sequence the gateway expects,
	// not just a convenience: skipping it leaves the session incomplete and
	// the gateway closes the tunnel request without a response.
	logf("fetching the tunnel configuration")
	tunnelConfig, err := forti.GetConfig(ctx, options.Gateway, options.Cookie)
	if err != nil {
		// A configuration that will not parse still means the gateway
		// answered, which is what the sequence needs. Report it and carry on
		// with whatever was read.
		logf("could not read the tunnel configuration: %v", err)
	}

	// The raw document is the only way to tell "the gateway published no
	// split routes" apart from "the parser did not recognise the shape it
	// used", and the two call for opposite fixes.
	if len(tunnelConfig.Raw) > 0 {
		logf("tunnel configuration: %s", tunnelConfig.Raw)
	}

	routes := options.Routes
	if routes == nil {
		// The gateway is the authority on what belongs to the VPN, so its
		// split-tunnel list is the default. An explicit empty list in Options
		// still means "install nothing".
		routes = tunnelConfig.SplitInclude
	}
	switch {
	case len(routes) == 0 && options.FullTunnel:
		logf("no split routes published; sending all traffic through the tunnel")
	case len(routes) == 0:
		logf("no routes to install: traffic will not use the tunnel unless routed there by hand")
	default:
		logf("routing %d network(s) through the tunnel", len(routes))
	}

	logf("opening the tunnel")
	link, err := dial(ctx)
	if err != nil {
		return nil, err
	}

	connection := &Connection{
		link:   link,
		config: configurator,
		logf:   logf,
		done:   make(chan struct{}),
	}

	// From here on every failure has something to undo.
	defer func() {
		if err != nil {
			_ = connection.teardown()
		}
	}()

	logf("negotiating PPP")
	session := ppp.NewSession(deadlineLink{link: link, timeout: readTimeout})
	session.Logf = logf
	connection.session = session

	negotiated, err := session.Negotiate(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "negotiating PPP")
	}

	device, err := newTUN(options.InterfaceName, mtu)
	if err != nil {
		return nil, err
	}
	connection.device = device

	name, err := device.Name()
	if err != nil {
		return nil, errors.Wrapf(err, "reading the interface name")
	}

	connection.details = Details{
		Interface: name,
		LocalIP:   negotiated.LocalIP,
		PeerIP:    negotiated.RemoteIP,
		DNS:       dnsList(negotiated),
		Routes:    routes,
	}

	logf("configuring %s with %s", name, negotiated.LocalIP)

	settings := netcfg.Settings{
		Interface:  name,
		LocalIP:    negotiated.LocalIP,
		PeerIP:     negotiated.RemoteIP,
		MTU:        mtu,
		DNS:        connection.details.DNS,
		DNSDomains: dnsDomains(options, tunnelConfig),
		FullTunnel: options.FullTunnel && len(routes) == 0,
		Routes:     routes,
		GatewayIP:  gatewayIP(options.Gateway, negotiated.RemoteIP),
	}

	if err = configurator.Apply(settings); err != nil {
		return nil, errors.Wrapf(err, "configuring the network")
	}

	if reporter, ok := configurator.(netcfg.ConflictReporter); ok {
		if conflicts := reporter.Conflicts(); len(conflicts) > 0 {
			// These destinations stay reachable, just not through the VPN.
			// Silence here would look like a company network being down.
			logf("%d route(s) installed; %d left on their existing path because the machine already reaches them: %s",
				reporter.InstalledRoutes(), len(conflicts), strings.Join(conflicts, ", "))
		}
	}

	// The data plane outlives Connect, so it gets its own context rather than
	// the caller's, which is only meant to bound the setup. WithoutCancel
	// keeps the caller's values — the trace id among them — while dropping its
	// cancellation.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	connection.cancel = cancel

	connection.wg.Add(2)
	go connection.pumpTunnelToDevice(runCtx)
	go connection.pumpDeviceToTunnel(runCtx)

	return connection, nil
}

// Details returns what the gateway assigned.
func (c *Connection) Details() Details { return c.details }

// Counters reports the bytes carried in each direction.
func (c *Connection) Counters() (in, out int64) {
	return c.bytesIn.Load(), c.bytesOut.Load()
}

// Done is closed when the connection stops, whether it was closed or dropped.
func (c *Connection) Done() <-chan struct{} { return c.done }

// Err reports why the connection stopped, or nil if it was closed normally.
func (c *Connection) Err() error {
	if failure := c.failure.Load(); failure != nil {
		return *failure
	}

	return nil
}

// Close tears the connection down and restores the network configuration.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		// Unblock a read that is parked on the deadline so the tunnel pump can
		// exit on its own.
		if c.link != nil {
			_ = c.link.SetReadDeadline(time.Now())
		}

		// The device pump cannot be woken the same way: a tun read blocks until
		// a packet arrives, and a cancelled context is only noticed at the top
		// of the loop. Closing the device is the only thing that releases it,
		// so the teardown has to run before the wait rather than after it —
		// waiting first deadlocks on any interface that happens to be quiet,
		// which is every interface with no traffic on it.
		c.closeErr = c.teardown()
		c.wg.Wait()
	})

	return c.closeErr
}

// teardown releases whatever has been created so far, in reverse order.
func (c *Connection) teardown() error {
	var problems []error

	if c.config != nil {
		if err := c.config.Revert(); err != nil {
			problems = append(problems, err)
		}
	}
	if c.device != nil {
		if err := c.device.Close(); err != nil {
			problems = append(problems, errors.Wrapf(err, "closing the interface"))
		}
	}
	if c.link != nil {
		if err := c.link.Close(); err != nil {
			problems = append(problems, errors.Wrapf(err, "closing the tunnel"))
		}
	}

	c.markDone(nil)

	return errors.Join(problems...)
}

// markDone records why the connection stopped and wakes anyone waiting.
func (c *Connection) markDone(cause error) {
	c.doneOnce.Do(func() {
		if cause != nil {
			c.failure.Store(&cause)
		}
		close(c.done)
	})
}

// pumpTunnelToDevice moves packets from the gateway into the interface, and
// answers the control traffic that keeps the link alive.
func (c *Connection) pumpTunnelToDevice(ctx context.Context) {
	defer c.wg.Done()

	for {
		if ctx.Err() != nil {
			return
		}

		if err := c.link.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			c.stop(errors.Wrapf(err, "setting the read deadline"))
			return
		}

		frame, err := c.link.ReadPacket()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.stop(errors.Wrapf(err, "reading from the tunnel"))
			return
		}

		packet, err := ppp.Decode(frame)
		if err != nil {
			c.logf("dropping an undecodable frame: %v", err)
			continue
		}

		if packet.Protocol != ppp.ProtocolIPv4 {
			// Keepalives and renegotiation live here; ignoring them costs the
			// link after a minute or two.
			if err := c.session.HandleControl(packet); err != nil {
				if ctx.Err() != nil {
					return
				}
				c.stop(err)
				return
			}
			continue
		}

		written, err := c.device.Write(packet.Data)
		if err != nil {
			// Close tears the device down while the pumps are still running, so
			// a write that loses that race is the shutdown, not a failure.
			if ctx.Err() != nil {
				return
			}
			c.stop(errors.Wrapf(err, "writing to the interface"))
			return
		}
		c.bytesIn.Add(int64(written))
	}
}

// pumpDeviceToTunnel moves packets from the interface out to the gateway.
func (c *Connection) pumpDeviceToTunnel(ctx context.Context) {
	defer c.wg.Done()

	mtu, err := c.device.MTU()
	if err != nil || mtu <= 0 {
		mtu = defaultMTU
	}
	buffer := make([]byte, mtu+128)

	for {
		if ctx.Err() != nil {
			return
		}

		n, err := c.device.Read(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.stop(errors.Wrapf(err, "reading from the interface"))
			return
		}
		if n == 0 {
			continue
		}

		packet := ppp.Packet{Protocol: ppp.ProtocolIPv4, Data: buffer[:n]}
		if err := c.link.WritePacket(packet.Encode()); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.stop(errors.Wrapf(err, "writing to the tunnel"))
			return
		}
		c.bytesOut.Add(int64(n))
	}
}

// stop records a data-plane failure and wakes anyone waiting on Done.
func (c *Connection) stop(cause error) {
	c.logf("connection stopping: %v", cause)
	c.markDone(cause)
}

// deadlineLink bounds every read during negotiation, so a gateway that stops
// answering surfaces as an error instead of hanging.
type deadlineLink struct {
	link    Link
	timeout time.Duration
}

func (l deadlineLink) ReadPacket() ([]byte, error) {
	if err := l.link.SetReadDeadline(time.Now().Add(l.timeout)); err != nil {
		return nil, errors.Wrapf(err, "setting the link read deadline")
	}

	packet, err := l.link.ReadPacket()

	return packet, errors.Wrapf(err, "reading from the link")
}

func (l deadlineLink) WritePacket(packet []byte) error {
	return errors.Wrapf(l.link.WritePacket(packet), "writing to the link")
}

// dnsDomains picks the domains to resolve through the tunnel, preferring what
// the caller configured over what the gateway publishes.
func dnsDomains(options Options, tunnelConfig forti.TunnelConfig) []string {
	if len(options.DNSDomains) > 0 {
		return options.DNSDomains
	}
	if tunnelConfig.DNSSuffix != "" {
		return []string{tunnelConfig.DNSSuffix}
	}

	return nil
}

func dnsList(config ppp.Config) []net.IP {
	var servers []net.IP
	for _, server := range []net.IP{config.PrimaryDNS, config.SecondDNS} {
		if server != nil && !server.IsUnspecified() {
			servers = append(servers, server)
		}
	}

	return servers
}

// gatewayIP resolves the public address of the gateway, which must keep its
// original route.
//
// The peer address PPP reports is often the gateway's own public address, but
// resolving the configured host is authoritative and does not depend on that.
func gatewayIP(gw forti.Gateway, peer net.IP) net.IP {
	if addrs, err := net.LookupIP(gw.Host); err == nil {
		for _, addr := range addrs {
			if v4 := addr.To4(); v4 != nil {
				return v4
			}
		}
	}

	return peer
}
