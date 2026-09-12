// Command svpn is the command-line client for the svpn VPN.
//
// It holds no privilege: everything that needs root — the tun device, the
// routing table, DNS — happens in svpnd, and this asks for it over the
// control socket. The commands themselves are internal/client/cli, a sibling
// of internal/client/gui: two front ends over one client.
package main

import "github.com/paccolamano/svpn/internal/client/cli"

func main() {
	cli.Execute()
}
