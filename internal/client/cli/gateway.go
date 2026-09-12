package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/pflag"

	"github.com/paccolamano/svpn/internal/client/auth"
	"github.com/paccolamano/svpn/internal/probe"
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
	flags.StringVar(&o.userAgent, "user-agent", "", "User-Agent presented to the gateway; empty sends the default, which mirrors openfortivpn")
	flags.BoolVar(&o.insecure, "insecure", false, "skip TLS verification; for probing an untrusted certificate, never for real traffic")
}

// gateway names the gateway for a probe. The FortiGate protocol itself is not
// reachable from here: internal/probe and internal/client/auth are the two
// packages allowed to speak it, and each takes a description rather than a
// forti.Gateway.
func (o gatewayOptions) gateway() probe.Gateway {
	return probe.Gateway{
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

// authOptions turns the gateway and SAML flags into a login.
func (o gatewayOptions) authOptions(saml samlOptions) auth.Options {
	return auth.Options{
		Host:      o.host,
		Port:      o.port,
		Realm:     o.realm,
		UserAgent: o.userAgent,
		Insecure:  o.insecure,
		Debug:     svpnOpts.debug,
		SAMLPort:  saml.port,
		NoBrowser: saml.noBrowser,
	}
}

// authenticate runs the SAML flow and returns the session.
//
// It happens on this side rather than in the daemon because it needs the
// user's browser, and a system service has neither a browser nor a display to
// open one on. The cookie is all the daemon ever sees of the user's identity.
//
// The flow itself is internal/client/auth, the same one the desktop client
// runs. This used to call forti.SAMLLogin directly, which made two
// implementations of a login whose details — the loopback callback, the
// validated session id, the cookie taken verbatim — are each easy to get
// subtly wrong in a different way.
//
// A whole auth.Session comes back rather than the cookie alone: a cookie is
// only valid for the gateway that issued it, and carrying the two together is
// what stops a caller authenticating against one gateway and connecting to
// another.
func authenticate(ctx context.Context, gw gatewayOptions, saml samlOptions) (auth.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, saml.timeout)
	defer cancel()

	options := gw.authOptions(saml)
	options.OnURL = func(url string) {
		fmt.Printf("Authenticate at:\n  %s\n\n", url)
		fmt.Printf("Waiting for the identity provider to call back on http://127.0.0.1:%d/ ...\n", saml.port)
	}

	fmt.Printf("Gateway: %s\n", gw.gateway().Addr())

	session, err := auth.Login(ctx, options)

	return session, errors.Wrapf(err, "logging in")
}
