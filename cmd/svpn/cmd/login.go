package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
)

var cmdLogin = &cobra.Command{
	Use:     "login",
	Short:   "Authenticate and print the session cookie",
	Version: cmd.Version,
	Long: `Authenticate against the gateway and print the session cookie.

up does this on its own, so this command is for the cases where the two steps
need separating: scripting a connection, or holding a cookie across several
attempts while something downstream is being debugged.

The cookie is a live credential — anyone holding it can open the VPN as you
until it expires.`,
	Run: run(login),
}

type loginOptions struct {
	gateway gatewayOptions
	saml    samlOptions
}

var loginOpts loginOptions

func init() {
	flags := cmdLogin.Flags()

	loginOpts.gateway.register(flags)
	loginOpts.saml.register(flags)

	cmdSVPN.AddCommand(cmdLogin)
}

func login(c *cobra.Command, args []string) error {
	cookie, err := authenticate(c.Context(), loginOpts.gateway.gateway(), loginOpts.saml)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Session cookie:")
	fmt.Println(cookie)

	return nil
}
