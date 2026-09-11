package cmd

import (
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/pkg/ipc"
)

var cmdStatus = &cobra.Command{
	Use:     "status",
	Short:   "Report the connection status",
	Version: cmd.Version,
	Long: `Report the connection status.

Shows the state, the address and DNS servers the gateway assigned, the routes
installed and the traffic counters. After a failure the reason is kept, so a
disconnected state still explains itself.`,
	Run: run(status),
}

type statusOptions struct {
	daemon daemonOptions
}

var statusOpts statusOptions

func init() {
	statusOpts.daemon.register(cmdStatus.Flags())

	cmdSVPN.AddCommand(cmdStatus)
}

func status(c *cobra.Command, args []string) error {
	return ask(statusOpts.daemon, ipc.Request{Command: ipc.CommandStatus})
}
