// Package auth performs the browser login that authenticates a VPN session and
// returns the credential the daemon needs to build the tunnel.
//
// It is public because the login is the one part of connecting that cannot
// happen inside svpnd: the daemon runs as a system service with no browser and
// no display, so whoever has the user's browser session has to do it and hand
// the result over. That is the CLI today and a desktop GUI tomorrow, and a GUI
// lives in another module, which cannot reach internal/forti.
//
// Only the login is exposed. The FortiGate protocol underneath it stays
// internal, so it remains free to change.
package auth

import (
	"context"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/browser"
	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/sorint"
)

// Options configure a login. The zero value authenticates against the
// Sorint.LAB gateway using the system browser, which is what a client that
// has no reason to care should pass.
type Options struct {
	// Host and Port identify the gateway. Empty and zero mean the Sorint.LAB
	// gateway.
	Host string
	Port int
	// Realm is optional, and only some gateways use one.
	Realm string
	// SAMLPort is the loopback port the identity provider redirects back to.
	// Zero means the Sorint.LAB callback port. It is not a free choice: the
	// gateway is configured with one exact callback and refuses any other.
	SAMLPort int
	// UserAgent overrides what is presented to the gateway. Empty is the
	// default, which mirrors what openfortivpn sends.
	UserAgent string
	// Insecure disables TLS verification. It exists for probing a gateway
	// whose certificate is not trusted locally, and must not be used for
	// anything carrying real traffic.
	Insecure bool

	// OpenBrowser is called with the authentication URL. Nil opens the
	// system browser, which is almost always right: it already holds the
	// user's identity-provider session, so authenticating is usually one
	// click rather than a full credential and MFA round.
	//
	// A GUI that has its own opener — Wails, for one — passes it here.
	// Opening the URL in an embedded webview instead is a trap: identity
	// providers routinely refuse to sign in from one, and a webview carries
	// its own cookie jar, so the existing session is lost.
	OpenBrowser func(url string) error
	// NoBrowser reports the URL through OnURL and opens nothing, for a client
	// that wants the user to follow the link by hand.
	NoBrowser bool
	// OnURL receives the authentication URL before the browser is opened. A
	// GUI uses it to show what it is waiting for, since the wait lasts as long
	// as the user takes to authenticate.
	OnURL func(url string)
}

// Session is the credential produced by a successful login.
//
// The gateway is carried alongside the cookie deliberately: a cookie is only
// valid for the gateway that issued it, and a client that authenticates
// against one gateway and then asks the daemon to connect to another gets a
// failure that looks like a rejected credential. Passing this whole value on
// makes that mismatch impossible to express.
type Session struct {
	// Cookie is a live credential. Anyone holding it can open the VPN as this
	// user until it expires, so it must not be logged or persisted in the
	// clear.
	Cookie string
	// Host and Port are the gateway the cookie is valid for.
	Host string
	Port int
}

// Login authenticates the user and returns the session credential.
//
// It blocks until the identity provider calls back, which takes as long as the
// user takes: bound it with a context deadline, and cancel the context to
// abandon a login the user has walked away from.
func Login(ctx context.Context, options Options) (Session, error) {
	options = options.withDefaults()

	open := options.OpenBrowser
	if options.NoBrowser {
		open = nil
	}

	gateway := forti.Gateway{
		Host:      options.Host,
		Port:      options.Port,
		Realm:     options.Realm,
		UserAgent: options.UserAgent,
		Insecure:  options.Insecure,
	}

	cookie, err := forti.SAMLLogin(ctx, gateway, options.SAMLPort, open, options.OnURL)
	if err != nil {
		return Session{}, errors.Wrapf(err, "authenticating against %s", gateway.Addr())
	}

	return Session{Cookie: cookie, Host: options.Host, Port: options.Port}, nil
}

// withDefaults fills in what the caller left unset.
func (o Options) withDefaults() Options {
	if o.Host == "" {
		o.Host = sorint.Host
	}
	if o.Port == 0 {
		o.Port = sorint.Port
	}
	if o.SAMLPort == 0 {
		o.SAMLPort = sorint.SAMLPort
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = browser.Open
	}

	return o
}
