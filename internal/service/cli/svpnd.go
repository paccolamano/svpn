// Package cmd implements svpnd, the privileged half of svpn.
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
The privileged service behind svpn

Connecting a VPN means creating a network interface, rewriting the routing
table and redirecting DNS. All three are global to the machine and gated behind
CAP_NET_ADMIN, and a desktop client has no terminal to type a sudo password
into. So the privilege is taken once, at install time, by this service — and
everything else runs as an ordinary user and asks it to act.

svpnd never authenticates anyone. The client owns the user's browser session,
performs the SAML login and hands over the resulting cookie; the cookie is all
of the user's identity this service ever sees.

Normally started by systemd, from the unit "svpnd install" writes.
`
	banner = `
  ___          _     _    __   _____ _  _ ___  
 / __| ___ _ _(_)_ _| |_  \ \ / / _ \ \| |   \ 
 \__ \/ _ \ '_| | ' \  _|  \ V /|  _/ .  | |) |
 |___/\___/_| |_|_||_\__|   \_/ |_| |_|\_|___/ `
)

var cmdSVPND = &cobra.Command{
	Use:     "svpnd",
	Short:   fmt.Sprintf("%s ver: %s\n%s", banner, build.Version, description),
	Version: build.Version,
	PersistentPreRun: func(c *cobra.Command, args []string) {
		logger, logLevel := log.NewLogger("svpnd")
		logLevel.Set(slog.LevelInfo)

		if svpndOpts.debug {
			logLevel.Set(slog.LevelDebug)
		}
		if svpndOpts.detailedErrors {
			log.SetDetailedErrors(true)
		}

		slog.SetDefault(logger)
	},
	Run: run(serve),
}

type svpndOptions struct {
	debug          bool
	detailedErrors bool
}

var svpndOpts svpndOptions

func init() {
	flags := cmdSVPND.PersistentFlags()

	flags.BoolVarP(&svpndOpts.debug, "debug", "d", false, "debug; traces the protocol exchange, which includes the session cookie")
	flags.BoolVar(&svpndOpts.detailedErrors, "detailed-errors", false, "enabled detailed errors logging")
}

// run adapts a fallible command body to cobra's Run, which has no error to
// return, and gives every command the same exit behaviour.
func run(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) {
	return func(c *cobra.Command, args []string) {
		if err := fn(c, args); err != nil {
			// A cancelled context is a shutdown signal, not a failure.
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Failure(err)
			os.Exit(1)
		}
	}
}

// Execute runs the daemon, and exits the process with a non-zero status if it
// fails to start or to serve.
func Execute() {
	// SIGTERM is how systemd stops the service, and it has to reach the
	// teardown: a tunnel outlives the process that made it, so a daemon that
	// exits without reverting leaves the machine routing into nothing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmdSVPND.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
