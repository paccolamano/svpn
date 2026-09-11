package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/pflag"

	"github.com/paccolamano/svpn/internal/browser"
	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/sorint"
)

// gatewayOptions identify the gateway and how to reach it. They default to the
// Sorint.LAB gateway, so none of them normally needs to be given.
type gatewayOptions struct {
	host      string
	port      int
	realm     string
	userAgent string
	insecure  bool
}

func (o *gatewayOptions) register(flags *pflag.FlagSet) {
	flags.StringVar(&o.host, "host", sorint.Host, "gateway hostname")
	flags.IntVar(&o.port, "port", sorint.Port, "gateway port")
	flags.StringVar(&o.realm, "realm", "", "authentication realm, if the gateway uses one")
	flags.StringVar(&o.userAgent, "user-agent", forti.DefaultUserAgent, "User-Agent presented to the gateway")
	flags.BoolVar(&o.insecure, "insecure", false, "skip TLS verification; for probing an untrusted certificate, never for real traffic")
}

func (o gatewayOptions) gateway() forti.Gateway {
	return forti.Gateway{
		Host:      o.host,
		Port:      o.port,
		Realm:     o.realm,
		UserAgent: o.userAgent,
		Insecure:  o.insecure,
		Debug:     svpnOpts.debug,
	}
}

// samlOptions govern the browser round trip that authenticates the session.
type samlOptions struct {
	port      int
	noBrowser bool
	timeout   time.Duration
}

func (o *samlOptions) register(flags *pflag.FlagSet) {
	flags.IntVar(&o.port, "saml-port", sorint.SAMLPort, "loopback port the identity provider redirects back to")
	flags.BoolVar(&o.noBrowser, "no-browser", false, "print the URL instead of opening a browser")
	flags.DurationVar(&o.timeout, "login-timeout", 3*time.Minute, "how long to wait for the browser callback")
}

// authenticate runs the SAML flow and returns the session cookie.
//
// It happens here rather than in the daemon because it needs the user's
// browser, and a system service has neither a browser nor a display to open
// one on. The cookie is all the daemon ever sees of the user's identity.
func authenticate(ctx context.Context, gw forti.Gateway, saml samlOptions) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, saml.timeout)
	defer cancel()

	open := browser.Open
	if saml.noBrowser {
		open = nil
	}

	onURL := func(url string) {
		fmt.Printf("Authenticate at:\n  %s\n\n", url)
		fmt.Printf("Waiting for the identity provider to call back on http://127.0.0.1:%d/ ...\n", saml.port)
	}

	fmt.Printf("Gateway: %s\n", gw.Addr())

	cookie, err := forti.SAMLLogin(ctx, gw, saml.port, open, onURL)

	return cookie, errors.Wrapf(err, "authenticating against %s", gw.Addr())
}
