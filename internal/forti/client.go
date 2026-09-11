// Package forti speaks the FortiGate SSL VPN protocol: SAML authentication and
// the PPP-over-TLS tunnel.
//
// The protocol is not publicly specified. Everything here follows the
// behaviour of openfortivpn (GPL-3.0), which is the de-facto reference
// implementation; the individual steps are documented where they are built.
package forti

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strconv"
)

// Gateway identifies a FortiGate SSL VPN endpoint.
type Gateway struct {
	Host string
	Port int
	// Realm is optional and is sent as a query parameter during SAML login.
	Realm string
	// UserAgent overrides the identifier sent to the gateway. Empty means
	// DefaultUserAgent.
	UserAgent string
	// Insecure disables TLS certificate verification. It exists for probing an
	// endpoint whose certificate chain is not yet trusted locally and must not
	// be used for anything carrying real traffic.
	Insecure bool
	// Debug traces every request and response to stderr, including the session
	// cookie. It prints a credential, so it is opt-in.
	Debug bool
}

// debugf traces protocol details when Debug is set.
func (g Gateway) debugf(format string, args ...any) {
	if !g.Debug {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
}

// Addr returns the "host:port" the gateway is reached at.
func (g Gateway) Addr() string {
	return net.JoinHostPort(g.Host, strconv.Itoa(g.Port))
}

// BaseURL returns the https origin of the gateway.
func (g Gateway) BaseURL() string {
	return fmt.Sprintf("https://%s", g.Addr())
}

// tlsConfig returns the TLS settings used for every connection to the gateway.
func (g Gateway) tlsConfig() *tls.Config {
	return &tls.Config{
		ServerName: g.Host,
		//nolint:gosec // Guarded by an explicit opt-in flag, see Gateway.Insecure.
		InsecureSkipVerify: g.Insecure,
		MinVersion:         tls.VersionTLS12,
	}
}
