package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
)

var cmdDebugConnect = &cobra.Command{
	Use:     "connect",
	Short:   "Authenticate, then open the tunnel and negotiate PPP",
	Version: cmd.Version,
	Long: `Authenticate, then open the tunnel and negotiate PPP.

The whole protocol in one command: login, allocation, configuration, tunnel and
PPP negotiation, reporting what the gateway assigned at each step. It is what
svpn up does, minus everything that needs privileges.`,
	Run: run(debugConnect),
}

type debugConnectOptions struct {
	gateway gatewayOptions
	saml    samlOptions
	probe   probeOptions
}

var debugConnectOpts debugConnectOptions

func init() {
	flags := cmdDebugConnect.Flags()

	debugConnectOpts.gateway.register(flags)
	debugConnectOpts.saml.register(flags)
	debugConnectOpts.probe.register(flags)

	cmdDebug.AddCommand(cmdDebugConnect)
}

func debugConnect(c *cobra.Command, args []string) error {
	gw := debugConnectOpts.gateway.gateway()

	cookie, err := authenticate(c.Context(), gw, debugConnectOpts.saml)
	if err != nil {
		return err
	}

	// Printed so a failure in a later step can be retried with the tunnel
	// probe instead of authenticating again.
	fmt.Printf("\nSession cookie: %s\n\n", cookie)

	return probe(c.Context(), gw, cookie, debugConnectOpts.probe)
}
