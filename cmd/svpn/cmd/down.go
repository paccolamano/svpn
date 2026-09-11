package cmd

import (
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/pkg/ipc"
)

var cmdDown = &cobra.Command{
	Use:     "down",
	Short:   "Turn off the VPN",
	Version: cmd.Version,
	Long: `Turn off the VPN.

The daemon tears the tunnel down and reverts every change it made to the
machine's routing and DNS.`,
	Run: run(down),
}

type downOptions struct {
	daemon daemonOptions
}

var downOpts downOptions

func init() {
	downOpts.daemon.register(cmdDown.Flags())

	cmdSVPN.AddCommand(cmdDown)
}

func down(c *cobra.Command, args []string) error {
	return ask(downOpts.daemon, ipc.Request{Command: ipc.CommandDisconnect})
}
