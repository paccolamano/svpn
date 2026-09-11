package cmd

import (
	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
)

var cmdDebugTunnel = &cobra.Command{
	Use:     "tunnel",
	Short:   "Open the tunnel with an existing cookie and negotiate PPP",
	Version: cmd.Version,
	Long: `Open the tunnel with a cookie from svpn login and negotiate PPP.

Reaching "Link established" proves the gateway accepted the cookie and the
protocol works end to end. Nothing is plugged into the operating system
afterwards, so no traffic can flow through it.`,
	Run: run(debugTunnel),
}

type debugTunnelOptions struct {
	gateway gatewayOptions
	probe   probeOptions
	cookie  string
}

var debugTunnelOpts debugTunnelOptions

func init() {
	flags := cmdDebugTunnel.Flags()

	debugTunnelOpts.gateway.register(flags)
	debugTunnelOpts.probe.register(flags)
	flags.StringVar(&debugTunnelOpts.cookie, "cookie", "", "session cookie, as printed by svpn login (required)")

	cmdDebug.AddCommand(cmdDebugTunnel)
}

func debugTunnel(c *cobra.Command, args []string) error {
	if debugTunnelOpts.cookie == "" {
		return errors.New("--cookie is required; get one with svpn login")
	}

	return probe(c.Context(), debugTunnelOpts.gateway.gateway(), debugTunnelOpts.cookie, debugTunnelOpts.probe)
}
