package state

import (
	"context"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/client/auth"
	"github.com/paccolamano/svpn/internal/ipc"
)

// Daemon is the part of svpnd this controller uses. It is an interface so the
// controller can be driven without a daemon, a socket or root — the same seam
// vpn.Connect takes for Dial and NewTUN, and for the same reason.
//
// client.Client satisfies it as it is. There used to be a SocketDaemon here
// that dialled the socket itself, which is how the GUI ended up showing the
// bare dial error while svpn explained the group and the newgrp; both now go
// through the one implementation.
type Daemon interface {
	Do(request ipc.Request, timeout time.Duration) (ipc.Response, error)
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
