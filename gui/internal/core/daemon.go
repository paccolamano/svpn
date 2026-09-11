package core

import (
	"context"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/pkg/auth"
	"github.com/paccolamano/svpn/pkg/ipc"
)

// Daemon is the part of svpnd this client uses. It is an interface so the
// controller can be driven without a daemon, a socket or root — the same seam
// vpn.Connect takes for Dial and NewTUN, and for the same reason.
type Daemon interface {
	Send(request ipc.Request, timeout time.Duration) (ipc.Response, error)
}

// SocketDaemon talks to svpnd over its control socket.
type SocketDaemon struct {
	// Path is the control socket. Empty means ipc.DefaultSocket.
	Path string
}

// Send dials, sends one request and closes, which is what the CLI does for
// every command too. The daemon answers one command per connection, and a GUI
// that held the socket open across a suspend would find it dead exactly when
// the user came back to it.
func (d SocketDaemon) Send(request ipc.Request, timeout time.Duration) (ipc.Response, error) {
	path := d.Path
	if path == "" {
		path = ipc.DefaultSocket
	}

	client, err := ipc.NewClient(path)
	if err != nil {
		// Not "connecting to the daemon at <path>": ipc.Dial says exactly that
		// already, and this is the message a user reads in the window. Two
		// layers saying the same thing made it look like the error had
		// happened twice.
		return ipc.Response{}, errors.Wrapf(err, "sending the %s command", request.Command)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(request, timeout)

	return response, errors.Wrapf(err, "asking the daemon to %s", request.Command)
}

// Authenticator performs the browser login.
//
// It is a function rather than a direct call to pkg/auth so that the connect
// path can be tested end to end without opening a browser and without a real
// identity provider.
type Authenticator func(ctx context.Context, onURL func(url string)) (auth.Session, error)

// BrowserLogin authenticates through the system browser.
//
// The system browser, not an embedded webview: the user's identity-provider
// session already lives there, so this is usually one click instead of a full
// credential and MFA round — and providers routinely refuse to sign in from a
// webview anyway. pkg/auth says the same thing at more length.
func BrowserLogin(options auth.Options) Authenticator {
	return func(ctx context.Context, onURL func(url string)) (auth.Session, error) {
		options.OnURL = onURL

		session, err := auth.Login(ctx, options)

		return session, errors.Wrapf(err, "logging in")
	}
}
