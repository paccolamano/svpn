// Package sorint holds what is specific to the Sorint.LAB VPN.
//
// Everything else in this repository is a generic FortiGate SSL VPN client:
// internal/forti speaks the protocol, internal/vpn moves the packets, and
// neither knows which company's gateway is on the other end. The values that
// do encode that live here, in one place, so `svpn up` needs no flags at all.
package sorint

const (
	// Host is the corporate gateway.
	Host = "vpn.sorint.it"
	// Port is where it serves the SSL VPN.
	Port = 443
	// SAMLPort is the loopback port the identity provider redirects back to.
	// It is not a free choice: the gateway is configured with this exact
	// callback, and a different one is refused.
	SAMLPort = 8020
	// Interface is the name given to the tunnel device.
	Interface = "svpn0"
	// SocketGroup is handed the daemon's control socket at install time, so
	// members can drive the VPN without being root.
	SocketGroup = "svpn"
)
