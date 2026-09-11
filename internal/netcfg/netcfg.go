// Package netcfg applies the addresses, routes and DNS a negotiated tunnel
// needs, and puts the system back as it found it.
//
// Every operation here needs elevated privileges, which is the whole reason
// the daemon exists as a separate process.
package netcfg

import "net"

// Settings is what a negotiated link asks the operating system for.
type Settings struct {
	// Interface is the tunnel device, as named by the operating system.
	Interface string
	LocalIP   net.IP
	PeerIP    net.IP
	MTU       int
	DNS       []net.IP
	// DNSDomains are resolved through the tunnel's servers. Empty leaves the
	// rest of resolution on the system's existing servers.
	DNSDomains []string
	// FullTunnel reports that every destination goes through the tunnel, which
	// means every name has to resolve through it too.
	FullTunnel bool
	// Routes are the destinations to send through the tunnel. An empty list
	// means the caller wants no routes installed.
	Routes []net.IPNet
	// GatewayIP is the public address of the VPN gateway.
	//
	// It must keep reaching the network through the original route: sending it
	// through the tunnel would route the tunnel through itself and the
	// connection would stall the moment it came up.
	GatewayIP net.IP
}

// ConflictReporter is implemented by configurators that can say which of the
// requested routes were left on their existing path.
//
// It is a separate interface because not every platform can report it, and a
// caller that cares should degrade rather than require it.
type ConflictReporter interface {
	// Conflicts lists networks the machine already reached another way.
	Conflicts() []string
	// InstalledRoutes counts the routes actually added for the tunnel.
	InstalledRoutes() int
}

// Configurator applies and reverts network settings.
//
// Apply must be safe to call once per connection; Revert must tolerate being
// called after a partial Apply, and after a previous Revert, so that shutdown
// paths can always run it.
type Configurator interface {
	Apply(settings Settings) error
	Revert() error
}
