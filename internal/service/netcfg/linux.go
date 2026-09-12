//go:build linux

package netcfg

import (
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/sorintlab/errors"
)

// New returns the configurator for this platform.
func New() Configurator { return &linuxConfigurator{} }

// halfInternetRoutes covers the whole IPv4 space in two halves.
//
// They are more specific than the system's default route, so the tunnel
// captures everything without the default route being touched — which means
// nothing has to be remembered and restored, and a crashed daemon leaves the
// original path intact once the interface disappears.
var halfInternetRoutes = []string{"0.0.0.0/1", "128.0.0.0/1"}

// linuxConfigurator drives the `ip` and `resolvectl` commands.
//
// Shelling out rather than speaking netlink directly is a deliberate trade for
// a first implementation: it is far less code, and every step is a command an
// operator can run by hand to see the same result.
type linuxConfigurator struct {
	applied  bool
	settings Settings
	// gatewayRouteAdded records whether the host route protecting the tunnel
	// was ours to remove; a route that already existed must be left alone.
	gatewayRouteAdded bool
	dnsApplied        bool
	// installedRoutes and conflicts record what happened to the gateway's
	// list, so a caller can report which networks the tunnel does not carry.
	installedRoutes int
	conflicts       []string
}

// Conflicts returns the networks left on their existing route because the
// machine already reached them another way.
func (c *linuxConfigurator) Conflicts() []string { return c.conflicts }

// InstalledRoutes returns how many routes were added for the tunnel.
func (c *linuxConfigurator) InstalledRoutes() int { return c.installedRoutes }

func (c *linuxConfigurator) Apply(settings Settings) error {
	c.settings = settings

	mtu := settings.MTU
	if mtu <= 0 {
		mtu = 1400
	}

	// Pin the gateway before anything else can claim it. The tunnel carries
	// itself over this route, so a tunnel route capturing it stalls the
	// connection the moment it comes up.
	if err := c.pinGatewayRoute(); err != nil {
		return err
	}

	// The address is added as a /32 with no peer. Naming a peer would make the
	// kernel install a route to it through this interface, and the peer a
	// FortiGate reports is its own public address — which is precisely the
	// route that must not go through the tunnel.
	if err := run("ip", "addr", "add", settings.LocalIP.String()+"/32", "dev", settings.Interface); err != nil {
		return err
	}
	c.applied = true

	if err := run("ip", "link", "set", "dev", settings.Interface,
		"mtu", strconv.Itoa(mtu), "up"); err != nil {
		return err
	}

	if err := c.addRoutes(); err != nil {
		return err
	}

	// Now that routes exist, confirm the gateway is still reachable off the
	// tunnel. Getting this wrong breaks the connection in a way that is hard
	// to attribute later, so it is checked rather than assumed.
	if err := c.verifyGatewayIsNotTunnelled(); err != nil {
		return err
	}

	return c.applyDNS()
}

// addRoutes sends the gateway's networks through the tunnel, or the whole
// internet when it publishes none.
func (c *linuxConfigurator) addRoutes() error {
	if len(c.settings.Routes) > 0 {
		var installed, conflicted int

		for _, route := range c.settings.Routes {
			err := run("ip", "route", "add", route.String(), "dev", c.settings.Interface)
			if err == nil {
				installed++
				continue
			}

			// A destination the machine already reaches — a docker bridge, the
			// local LAN — keeps the route it has. The gateway publishes
			// hundreds of networks and some are bound to overlap, so one
			// clash cannot be allowed to cost the whole connection. The
			// destination stays reachable locally, not through the VPN.
			if strings.Contains(err.Error(), "File exists") {
				c.conflicts = append(c.conflicts, route.String())
				conflicted++
				continue
			}

			return err
		}

		c.installedRoutes = installed
		if conflicted > 0 {
			// Not an error, but the user needs to know which company networks
			// are not going through the tunnel.
			return nil
		}

		return nil
	}

	if !c.settings.FullTunnel {
		// Taking over every destination is never implicit: a gateway that
		// publishes no split routes often does not route to the internet
		// either, and capturing everything would cut off every site outside
		// the company.
		return nil
	}

	for _, route := range halfInternetRoutes {
		if err := run("ip", "route", "add", route, "dev", c.settings.Interface); err != nil {
			return err
		}
	}

	return nil
}

// pinGatewayRoute adds a host route to the VPN gateway over the interface that
// currently reaches it.
func (c *linuxConfigurator) pinGatewayRoute() error {
	if c.settings.GatewayIP == nil {
		return nil
	}

	via, dev, err := routeTo(c.settings.GatewayIP)
	if err != nil {
		return err
	}
	if dev == c.settings.Interface {
		return errors.Errorf("the gateway is already routed through %s; refusing to build a loop", dev)
	}

	args := []string{"route", "add", c.settings.GatewayIP.String() + "/32"}
	if via != "" {
		args = append(args, "via", via)
	}
	args = append(args, "dev", dev)

	if err := run("ip", args...); err != nil {
		// An existing route is the desired state, so treat it as success but
		// remember not to remove someone else's route on the way out. The
		// check after the routes are installed confirms it actually holds.
		if strings.Contains(err.Error(), "File exists") {
			return nil
		}
		return err
	}

	c.gatewayRouteAdded = true

	return nil
}

// verifyGatewayIsNotTunnelled fails if traffic to the gateway would go through
// the tunnel.
func (c *linuxConfigurator) verifyGatewayIsNotTunnelled() error {
	if c.settings.GatewayIP == nil {
		return nil
	}

	_, dev, err := routeTo(c.settings.GatewayIP)
	if err != nil {
		return err
	}
	if dev == c.settings.Interface {
		return errors.Errorf(
			"traffic to the gateway %s would go through %s, which would carry the tunnel over itself",
			c.settings.GatewayIP, dev)
	}

	return nil
}

// applyDNS points name resolution at the servers the gateway supplied.
func (c *linuxConfigurator) applyDNS() error {
	if len(c.settings.DNS) == 0 {
		return nil
	}

	if _, err := exec.LookPath("resolvectl"); err != nil {
		// Without systemd-resolved there is no per-interface DNS to set, and
		// rewriting /etc/resolv.conf behind the system's back is worse than
		// saying so. Names will not resolve through the VPN.
		return errors.Wrapf(err, "resolvectl is unavailable, so DNS was not configured")
	}

	args := []string{"dns", c.settings.Interface}
	for _, server := range c.settings.DNS {
		args = append(args, server.String())
	}
	if err := run("resolvectl", args...); err != nil {
		return err
	}

	// A routing domain ("~name") sends queries for that suffix to this
	// interface's servers. Which domains to claim depends on how much of the
	// network the tunnel carries.
	domains := c.settings.DNSDomains
	if c.settings.FullTunnel {
		// Everything is routed through the tunnel, so resolving anywhere else
		// would send queries down a path that no longer reaches a resolver.
		domains = []string{"."}
	}

	if len(domains) == 0 {
		// A split tunnel with no domain to claim: the servers are registered
		// for the interface, and the rest of resolution stays where it was.
		c.dnsApplied = true

		return nil
	}

	args = []string{"domain", c.settings.Interface}
	for _, domain := range domains {
		args = append(args, "~"+strings.TrimPrefix(domain, "~"))
	}

	if err := run("resolvectl", args...); err != nil {
		return err
	}

	c.dnsApplied = true

	return nil
}

func (c *linuxConfigurator) Revert() error {
	if !c.applied {
		return nil
	}

	var problems []string

	// Routes over the interface disappear with it, and so does the
	// per-interface DNS, so only the gateway pin has to be undone explicitly.
	if c.gatewayRouteAdded && c.settings.GatewayIP != nil {
		if err := run("ip", "route", "del", c.settings.GatewayIP.String()+"/32"); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if err := run("ip", "link", "set", "dev", c.settings.Interface, "down"); err != nil {
		problems = append(problems, err.Error())
	}

	c.applied = false
	c.dnsApplied = false

	if len(problems) > 0 {
		return errors.Errorf("reverting the network configuration: %s", strings.Join(problems, "; "))
	}

	return nil
}

// routeTo reports how the system currently reaches an address.
func routeTo(ip net.IP) (via, dev string, err error) {
	output, err := exec.Command("ip", "route", "get", ip.String()).Output()
	if err != nil {
		return "", "", errors.Wrapf(err, "looking up the route to %s", ip)
	}

	via, dev = parseRouteGet(string(output))
	if dev == "" {
		return "", "", errors.Errorf("could not tell which interface reaches %s from %q", ip, output)
	}

	return via, dev, nil
}

// parseRouteGet pulls the next hop and interface out of `ip route get` output,
// which looks like:
//
//	185.243.193.130 via 192.168.1.1 dev wlp0s20f3 src 192.168.1.78 uid 1000
func parseRouteGet(output string) (via, dev string) {
	fields := strings.Fields(output)
	for i := 0; i+1 < len(fields); i++ {
		switch fields[i] {
		case "via":
			via = fields[i+1]
		case "dev":
			dev = fields[i+1]
		}
	}

	return via, dev
}

func run(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		// The kernel's own words carry the diagnosis ("File exists", "Operation
		// not permitted"); the exit status only says that something failed, so
		// it lands last as the wrapped cause.
		return errors.Wrapf(err, "%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))
	}

	return nil
}

var _ Configurator = (*linuxConfigurator)(nil)
