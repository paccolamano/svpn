package cmd

import (
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/internal/log"
)

var cmdDebug = &cobra.Command{
	Use:     "debug",
	Short:   "Protocol probes: no privileges, no interface, nothing configured",
	Version: cmd.Version,
	Long: `Protocol probes.

These speak the gateway's protocol and report what it says, without creating a
tunnel interface, touching routes or going near the daemon. They need no
privileges and change nothing, which makes them the right tool when the
connection fails and the question is where.

They are also the only part of svpn that runs on Windows and macOS today: the
daemon's interface and routing work is Linux-only so far, but the protocol
above it is portable.`,
	Run: func(c *cobra.Command, args []string) {
		if err := c.Help(); err != nil {
			log.Failure(err)
			os.Exit(1)
		}
	},
}

// probeOptions are shared by the probes that open a tunnel.
type probeOptions struct {
	packets    int
	idle       time.Duration
	dumpConfig bool
	passive    bool
}

func (o *probeOptions) register(flags *pflag.FlagSet) {
	flags.IntVar(&o.packets, "packets", 10, "stop after this many packets; 0 reads until interrupted")
	flags.DurationVar(&o.idle, "idle-timeout", 30*time.Second, "give up if no packet arrives within this window")
	flags.BoolVar(&o.dumpConfig, "dump-config", false, "print the raw XML configuration returned by the gateway")
	flags.BoolVar(&o.passive, "passive", false, "do not send the opening LCP packet, so the gateway stays silent")
}

func init() {
	cmdSVPN.AddCommand(cmdDebug)
}
