// Package client is the unprivileged half of svpn: what svpn and svpn-gui both
// do to reach the daemon.
//
// Everything here is a client of svpnd and nothing here holds privilege. The
// tun device, the routing table and DNS belong to the daemon, and the only
// thing that crosses between them is internal/ipc.
//
// The one exception is internal/client/auth, which speaks to the FortiGate
// directly. That is deliberate and it is why auth exists as a named boundary:
// the login needs a browser and the daemon is a system service with neither a
// browser nor a display, so the client has to do it and hand over the result.
// Past auth, the rest of this tree sees only an auth.Session.
package client

import (
	"os"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/build"
	"github.com/paccolamano/svpn/internal/ipc"
)

// Client sends commands to svpnd over its control socket.
//
// The zero value talks to ipc.DefaultSocket, which is what a client with no
// reason to care should use.
type Client struct {
	// Socket is the daemon's control socket. Empty means ipc.DefaultSocket.
	Socket string
}

// socket is the path Do will dial.
func (c Client) socket() string {
	if c.Socket == "" {
		return ipc.DefaultSocket
	}

	return c.Socket
}

// Do sends one request and returns the daemon's reply.
//
// It dials, sends and closes every time, which is what both clients want: the
// daemon answers one command per connection, and a GUI that held the socket
// open across a suspend would find it dead exactly when the user came back
// to it.
func (c Client) Do(request ipc.Request, timeout time.Duration) (ipc.Response, error) {
	socket := c.socket()

	conn, err := ipc.NewClient(socket)
	if err != nil {
		return ipc.Response{}, unreachable(err, socket)
	}
	defer func() { _ = conn.Close() }()

	response, err := conn.Do(request, timeout)

	return response, errors.Wrapf(err, "asking the daemon to %s", request.Command)
}

// unreachable explains why the daemon could not be reached.
//
// The dial error on its own is never the diagnosis: "no such file or
// directory" and "permission denied" are both true and neither says what to do.
// The two causes want opposite fixes, and telling them apart is the difference
// between a working installation and one that looks broken on first use.
//
// This used to live in the svpn command layer, where the GUI could not reach
// it: a desktop user hitting the newgrp case below — which is what happens on
// the very first launch after installing — saw the bare permission error and
// nothing about the group.
func unreachable(err error, socket string) error {
	if !errors.Is(err, os.ErrPermission) {
		return errors.Wrapf(err, "svpnd is not reachable; check the service is running (systemctl status svpnd)")
	}

	if group, pending := ipc.PendingGroup(socket); pending {
		return errors.Wrapf(err,
			"you are in the %s group but this session started before that, so it does not have it yet; run \"newgrp %s\" or log out and back in",
			group, group)
	}

	return errors.Wrapf(err,
		"this account may not open %s; add it to the group that owns the socket with \"sudo svpnd install\", and do not use sudo to work around this — the daemon refuses uid 0 on purpose",
		socket)
}

// Mismatch describes a daemon built from something other than this client.
//
// The two halves are separate binaries with a protocol between them, and since
// they can be updated separately — svpnd update replaces all three, but a
// desktop client installed on its own does not — a mismatch is something that
// happens rather than something that cannot. It is never a refusal: the
// protocol is newline-delimited JSON and tolerates a field the other side does
// not know, so the versions differing is usually survivable and always worth
// knowing about when something behaves oddly.
type Mismatch struct {
	// Client is this build.
	Client string
	// Daemon is what the daemon reports, and is empty when the daemon predates
	// version reporting.
	Daemon string
	// Hint is what to do about it.
	Hint string
}

// Message describes the mismatch in one line.
func (m Mismatch) Message() string {
	if m.Daemon == "" {
		return "the daemon predates version reporting, so it is older than this client"
	}

	return "the client and the daemon are different builds"
}

// CheckVersion compares a status reply against this build.
//
// It reports the mismatch rather than logging one, because the two clients say
// it differently: svpn writes a warning line to stderr and a window has
// somewhere better to put it.
func CheckVersion(status *ipc.Status) (Mismatch, bool) {
	if status == nil || build.Version == "" {
		return Mismatch{}, false
	}

	// An empty version is not "unknown": every build that reports one at all
	// sends something, even the "unknown" a plain `go build` leaves behind. So
	// a daemon saying nothing is one from before the field existed, which is a
	// mismatch this client can be certain about.
	if status.Version == "" {
		return Mismatch{
			Client: build.Version,
			Hint:   "restart it to pick up the installed binary: sudo systemctl restart svpnd",
		}, true
	}

	if status.Version != build.Version {
		return Mismatch{
			Client: build.Version,
			Daemon: status.Version,
			Hint:   "run \"sudo svpnd update\", or restart svpnd if it is still running an older binary",
		}, true
	}

	return Mismatch{}, false
}
