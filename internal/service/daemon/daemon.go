// Package daemon is the privileged service: it owns the VPN connection and
// serves commands from unprivileged clients.
package daemon

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ipc"
	"github.com/paccolamano/svpn/internal/service/vpn"
)

// Options configures the service.
type Options struct {
	// DefaultGateway is used when a client does not name one.
	DefaultGateway forti.Gateway
	// InterfaceName is the hint passed to the tunnel device.
	InterfaceName string
	// Routes, DNSDomains and FullTunnel shape what the tunnel carries; see
	// vpn.Options for what each one means.
	Routes     []net.IPNet
	DNSDomains []string
	FullTunnel bool
	// Version is reported to clients so they can tell they are talking to a
	// build other than their own. It arrives as an option rather than being
	// read from the cmd package, which this has no business importing.
	Version string
	Logf    func(format string, args ...any)
	// Connect opens a VPN connection. Tests replace it; nil selects vpn.Connect.
	Connect func(ctx context.Context, options vpn.Options) (Connection, error)
}

// Connection is the part of a VPN connection the daemon depends on, named as
// an interface so tests can supply their own.
type Connection interface {
	Details() vpn.Details
	Counters() (in, out int64)
	Done() <-chan struct{}
	Err() error
	Close() error
}

// ErrNotConnected is returned when a command needs a live connection and
// there is none.
var ErrNotConnected = errors.New("not connected")

// ErrConnectAborted is returned to the client that asked to connect when a
// disconnect arrived before the attempt finished.
var ErrConnectAborted = errors.New("the connection attempt was cancelled by a disconnect")

// Daemon owns at most one VPN connection at a time.
type Daemon struct {
	options Options
	logf    func(format string, args ...any)

	// mu guards everything below. Commands are rare and each one is short
	// apart from connect, so one lock is simpler than splitting the state.
	mu         sync.Mutex
	state      ipc.State
	since      time.Time
	lastError  string
	gateway    string
	connection Connection
	// watching is cancelled when the current connection is replaced, so a
	// stale watcher cannot overwrite the state of a newer one.
	watching context.CancelFunc
	// cancelConnect aborts an attempt that has not produced a connection yet.
	// It is set for exactly as long as the state is StateConnecting, and is
	// what lets a disconnect arriving mid-connect stop it.
	cancelConnect context.CancelFunc
}

// New returns a daemon in the disconnected state.
func New(options Options) *Daemon {
	logf := options.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	if options.Connect == nil {
		options.Connect = func(ctx context.Context, connectOptions vpn.Options) (Connection, error) {
			return vpn.Connect(ctx, connectOptions)
		}
	}

	return &Daemon{options: options, logf: logf, state: ipc.StateDisconnected}
}

// Handle serves one client command.
func (d *Daemon) Handle(ctx context.Context, peer ipc.Peer, request ipc.Request) ipc.Response {
	switch request.Command {
	case ipc.CommandStatus:
		return ipc.Response{OK: true, Status: d.status()}

	case ipc.CommandConnect:
		d.logf("connect requested by uid=%d pid=%d", peer.UID, peer.PID)
		if err := d.connect(ctx, request); err != nil {
			return ipc.Response{Error: err.Error(), Status: d.status()}
		}
		return ipc.Response{OK: true, Status: d.status()}

	case ipc.CommandDisconnect:
		d.logf("disconnect requested by uid=%d pid=%d", peer.UID, peer.PID)
		if err := d.disconnect(); err != nil {
			return ipc.Response{Error: err.Error(), Status: d.status()}
		}
		return ipc.Response{OK: true, Status: d.status()}

	default:
		return ipc.Response{Error: fmt.Sprintf("unknown command %q", request.Command)}
	}
}

func (d *Daemon) connect(ctx context.Context, request ipc.Request) error {
	if request.Cookie == "" {
		return errors.New("no session cookie: authenticate first and send the cookie with the request")
	}

	d.mu.Lock()
	switch d.state {
	case ipc.StateConnected, ipc.StateConnecting:
		d.mu.Unlock()
		return errors.New("already connected; disconnect first")
	case ipc.StateDisconnecting:
		// The previous connection is still being torn down. Starting now would
		// race the teardown for the same interface and routes.
		d.mu.Unlock()
		return errors.New("still disconnecting; try again in a moment")
	}

	gateway := d.options.DefaultGateway
	if request.Host != "" {
		gateway.Host = request.Host
	}
	if request.Port != 0 {
		gateway.Port = request.Port
	}
	if gateway.Host == "" {
		d.mu.Unlock()
		return errors.New("no gateway configured and none given in the request")
	}

	// Until this attempt produces a connection there is nothing for a
	// disconnect to close, so it stops the attempt through this instead.
	connectCtx, cancelConnect := context.WithCancel(ctx)
	defer cancelConnect()

	d.state = ipc.StateConnecting
	d.since = time.Now()
	d.lastError = ""
	d.gateway = net.JoinHostPort(gateway.Host, fmt.Sprint(gateway.Port))
	d.cancelConnect = cancelConnect
	d.mu.Unlock()

	connection, err := d.options.Connect(connectCtx, vpn.Options{
		Gateway:       gateway,
		Cookie:        request.Cookie,
		InterfaceName: d.options.InterfaceName,
		Routes:        d.options.Routes,
		DNSDomains:    d.options.DNSDomains,
		FullTunnel:    d.options.FullTunnel,
		Logf:          d.logf,
	})

	// Publishing the result and clearing cancelConnect happen in one critical
	// section on purpose. A disconnect that landed between the two would find
	// neither a connection to close nor an attempt to cancel, and would report
	// success while the VPN came up behind it.
	d.mu.Lock()
	// disconnect signals an abort by moving the state here, which is all it can
	// do while the attempt still owns everything it has built.
	aborted := d.state == ipc.StateDisconnecting
	d.cancelConnect = nil
	d.since = time.Now()

	switch {
	case aborted:
		// Stays in StateDisconnecting until whatever the attempt produced has
		// been undone, just below.
		d.lastError = ""

	case err != nil:
		d.state = ipc.StateError
		d.lastError = err.Error()

	default:
		// Detached from the request's context — the connection outlives the
		// command that made it — but WithoutCancel rather than Background so
		// the trace id the logger reads still reaches the watcher.
		watchCtx, cancelWatch := context.WithCancel(context.WithoutCancel(ctx))

		d.connection = connection
		d.state = ipc.StateConnected
		d.watching = cancelWatch

		// A link can drop without anyone asking, so the state has to follow it
		// rather than waiting for the next command to notice. Started under the
		// same lock, so it cannot observe a half-published connection.
		go d.watch(watchCtx, connection)
	}
	d.mu.Unlock()

	if aborted {
		// Cancelling does not always arrive in time: the attempt may have run
		// to completion and produced a live connection. The client that asked
		// to disconnect has already been told the VPN is going down, so it is
		// undone rather than kept.
		message := ""
		if err == nil {
			if closeErr := connection.Close(); closeErr != nil {
				message = closeErr.Error()
			}
		}
		d.settle(ipc.StateDisconnected, message)

		return ErrConnectAborted
	}

	return err
}

// settle records the state a finished attempt left the daemon in.
func (d *Daemon) settle(state ipc.State, lastError string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.state = state
	d.since = time.Now()
	d.lastError = lastError
}

// watch reflects an unexpected disconnection into the reported state.
func (d *Daemon) watch(ctx context.Context, connection Connection) {
	select {
	case <-ctx.Done():
		return
	case <-connection.Done():
	}

	d.mu.Lock()

	// A newer connection may have replaced this one already.
	if d.connection != connection {
		d.mu.Unlock()
		return
	}

	if err := connection.Err(); err != nil {
		d.logf("connection lost: %v", err)
		d.state = ipc.StateError
		d.lastError = err.Error()
	} else {
		d.state = ipc.StateDisconnected
	}

	d.connection = nil
	d.since = time.Now()
	d.mu.Unlock()

	// A link that drops on its own still leaves an interface and routes
	// behind. Without this the machine keeps sending traffic into a tunnel
	// that no longer carries it, and the user has to clean up by hand.
	if err := connection.Close(); err != nil {
		d.logf("could not tear down the lost connection: %v", err)
	}
}

func (d *Daemon) disconnect() error {
	d.mu.Lock()

	// An attempt is still running and owns everything it has built so far.
	// Cancelling it is the only way to stop it: reporting success here and
	// letting it run to completion would bring the VPN up moments after the
	// user asked for the opposite.
	if d.cancelConnect != nil {
		d.cancelConnect()
		d.state = ipc.StateDisconnecting
		d.since = time.Now()
		d.mu.Unlock()

		return nil
	}

	connection := d.connection
	if connection == nil {
		state := d.state
		d.mu.Unlock()

		if state == ipc.StateDisconnected {
			return ErrNotConnected
		}
		return nil
	}

	if d.watching != nil {
		d.watching()
		d.watching = nil
	}
	d.connection = nil
	d.state = ipc.StateDisconnecting
	d.mu.Unlock()

	err := connection.Close()

	d.mu.Lock()
	d.state = ipc.StateDisconnected
	d.since = time.Now()
	if err != nil {
		d.lastError = err.Error()
	}
	d.mu.Unlock()

	return errors.Wrapf(err, "closing the connection")
}

// Shutdown closes any live connection, restoring the network configuration.
//
// It must run before the process exits: a tunnel left configured would keep
// routes pointing at an interface that no longer has anything behind it.
//
// Stopping with nothing connected is the ordinary case, not a failure, so the
// absence of a connection is not reported as an error.
func (d *Daemon) Shutdown() error {
	if err := d.disconnect(); err != nil && !errors.Is(err, ErrNotConnected) {
		return err
	}

	return nil
}

func (d *Daemon) status() *ipc.Status {
	d.mu.Lock()
	defer d.mu.Unlock()

	status := &ipc.Status{
		State:     d.state,
		Since:     d.since,
		Version:   d.options.Version,
		Gateway:   d.gateway,
		LastError: d.lastError,
	}

	if d.connection != nil {
		details := d.connection.Details()
		status.Interface = details.Interface
		if details.LocalIP != nil {
			status.LocalIP = details.LocalIP.String()
		}
		if details.PeerIP != nil {
			status.PeerIP = details.PeerIP.String()
		}
		for _, server := range details.DNS {
			status.DNS = append(status.DNS, server.String())
		}
		for _, route := range details.Routes {
			status.Routes = append(status.Routes, route.String())
		}
		status.BytesIn, status.BytesOut = d.connection.Counters()
	}

	return status
}
