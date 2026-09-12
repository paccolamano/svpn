package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/internal/build"
	"github.com/paccolamano/svpn/internal/log"
)

const (
	description = `
Connect to the Sorint.LAB VPN with ease

svpn drives the connection; it does not own it. The privileged work — creating
the tunnel interface, rewriting routes, redirecting DNS — belongs to the svpnd
daemon, which runs as a system service. This command asks it to act.

Run svpn as yourself, never under sudo: the daemon identifies its callers by
uid and an allowlist of ordinary users will refuse root, correctly.
`
	banner = `
  ___          _     _    __   _____ _  _ 
 / __| ___ _ _(_)_ _| |_  \ \ / / _ \ \| |
 \__ \/ _ \ '_| | ' \  _|  \ V /|  _/ .  |
 |___/\___/_| |_|_||_\__|   \_/ |_| |_|\_|`
)

var cmdSVPN = &cobra.Command{
	Use:     "svpn",
	Short:   fmt.Sprintf("%s ver: %s\n%s", banner, build.Version, description),
	Version: build.Version,
	PersistentPreRun: func(c *cobra.Command, args []string) {
		logger, logLevel := log.NewLogger("svpn")
		logLevel.Set(slog.LevelInfo)

		if svpnOpts.debug {
			logLevel.Set(slog.LevelDebug)
		}
		if svpnOpts.detailedErrors {
			log.SetDetailedErrors(true)
		}

		slog.SetDefault(logger)
	},
	Run: func(c *cobra.Command, args []string) {
		if err := c.Help(); err != nil {
			log.Failure(err)
			os.Exit(1)
		}
	},
}

type svpnOptions struct {
	debug          bool
	detailedErrors bool
}

var svpnOpts svpnOptions

func init() {
	flags := cmdSVPN.PersistentFlags()

	flags.BoolVarP(&svpnOpts.debug, "debug", "d", false, "debug")
	flags.BoolVar(&svpnOpts.detailedErrors, "detailed-errors", false, "enabled detailed errors logging")
}

// run adapts a fallible command body to cobra's Run, which has no error to
// return, and gives every command the same exit behaviour.
func run(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) {
	return func(c *cobra.Command, args []string) {
		if err := fn(c, args); err != nil {
			// A cancelled context is the user pressing Ctrl-C, not a failure.
			if errors.Is(err, context.Canceled) {
				slog.Info("interrupted")
				return
			}
			log.Failure(err)
			os.Exit(1)
		}
	}
}

// Execute runs the client, and exits the process with a non-zero status if a
// command fails.
func Execute() {
	// Ctrl-C should close the tunnel rather than leave it half-open, and
	// should abandon a browser login rather than wait out its timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmdSVPN.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
