package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/internal/build"
	"github.com/paccolamano/svpn/internal/client/auth"
	"github.com/paccolamano/svpn/internal/ipc"
)

var cmdUp = &cobra.Command{
	Use:     "up",
	Short:   "Bring the VPN up",
	Version: build.Version,
	Long: `Bring the VPN up.

Opens a browser to authenticate against the gateway, then hands the resulting
session cookie to the daemon, which builds the tunnel. Pass --cookie to reuse
a cookie from a previous login and skip the browser entirely.`,
	Run: run(up),
}

type upOptions struct {
	daemon     daemonOptions
	gateway    gatewayOptions
	saml       samlOptions
	cookie     string
	showCookie bool
}

var upOpts upOptions

func init() {
	flags := cmdUp.Flags()

	upOpts.daemon.register(flags)
	upOpts.gateway.register(flags)
	upOpts.saml.register(flags)
	flags.StringVar(&upOpts.cookie, "cookie", "", "reuse this session cookie instead of authenticating")
	flags.BoolVar(&upOpts.showCookie, "show-cookie", false, "print the session cookie, so a failed attempt can be retried without logging in again")

	cmdSVPN.AddCommand(cmdUp)
}

func up(c *cobra.Command, args []string) error {
	// The gateway the cookie is valid for, which is not always the one the
	// flags name: --cookie reuses a session obtained earlier, and only the
	// login knows which gateway answered.
	session := auth.Session{
		Cookie: upOpts.cookie,
		Host:   upOpts.gateway.host,
		Port:   upOpts.gateway.port,
	}

	if session.Cookie == "" {
		var err error
		if session, err = authenticate(c.Context(), upOpts.gateway, upOpts.saml); err != nil {
			return err
		}
		fmt.Println()
	}

	if upOpts.showCookie {
		fmt.Printf("Session cookie: %s\n\n", session.Cookie)
	}

	// The host is always sent, never left to the daemon's default: the cookie
	// is only valid for the gateway that issued it, so the two must agree.
	return ask(upOpts.daemon, ipc.Request{
		Command: ipc.CommandConnect,
		Cookie:  session.Cookie,
		Host:    session.Host,
		Port:    session.Port,
	})
}
