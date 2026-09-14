// Command svpnd is the privileged daemon behind the svpn VPN.
//
// It owns everything a client cannot do for itself: the tun device, the
// routing table and DNS, plus its own installation and update. Its commands
// are internal/service/cli.
package main

import "github.com/paccolamano/svpn/internal/service/cli"

func main() {
	cli.Execute()
}
