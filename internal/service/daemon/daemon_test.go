package daemon

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ipc"
	"github.com/paccolamano/svpn/internal/service/vpn"
)

// fakeConnection stands in for a live tunnel. Creating one for real needs
// CAP_NET_ADMIN, which is exactly why the daemon takes it as an interface.
type fakeConnection struct {
	mu       sync.Mutex
	details  vpn.Details
	done     chan struct{}
	doneOnce sync.Once
	err      error
	closed   bool
	closeErr error
}

func newFakeConnection() *fakeConnection {
	return &fakeConnection{
		details: vpn.Details{
			Interface: "svpn0",
			LocalIP:   net.IPv4(10, 110, 248, 16),
			PeerIP:    net.IPv4(185, 243, 193, 130),
			DNS:       []net.IP{net.IPv4(10, 140, 9, 4)},
		},
		done: make(chan struct{}),
	}
}

func (f *fakeConnection) Details() vpn.Details     { return f.details }
func (f *fakeConnection) Counters() (int64, int64) { return 100, 200 }
func (f *fakeConnection) Done() <-chan struct{}    { return f.done }
func (f *fakeConnection) Err() error               { return f.err }
func (f *fakeConnection) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()

	// Close may follow a drop, which already closed the channel.
	f.doneOnce.Do(func() { close(f.done) })

	return f.closeErr
}

// closedFlag reports whether Close ran, read under the lock because the daemon
// closes a lost connection from its own goroutine.
func (f *fakeConnection) closedFlag() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closed
}

// drop simulates the link failing on its own.
func (f *fakeConnection) drop(cause error) {
	f.err = cause
	f.doneOnce.Do(func() { close(f.done) })
}

func newTestDaemon(t *testing.T, connect func(context.Context, vpn.Options) (Connection, error)) *Daemon {
	t.Helper()

	return New(Options{
		DefaultGateway: forti.Gateway{Host: "vpn.example.com", Port: 443},
		Version:        "v1.2.3",
		Connect:        connect,
	})
}

func TestStatusReportsTheDaemonVersion(t *testing.T) {
	d := newTestDaemon(t, nil)

	// A client compares this against its own build to notice that the two
	// halves were updated separately, so it has to be there even when nothing
	// is connected — which is when a mismatch is easiest to fix.
	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandStatus, ""))
	if response.Status == nil {
		t.Fatal("status is missing")
	}
	if response.Status.Version != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", response.Status.Version)
	}
}

func request(command, cookie string) ipc.Request {
	return ipc.Request{Command: command, Cookie: cookie}
}

func TestConnectReportsTheAssignedConfiguration(t *testing.T) {
	connection := newFakeConnection()
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return connection, nil
	})

	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "SVPNCOOKIE=x"))
	if !response.OK {
		t.Fatalf("connect failed: %s", response.Error)
	}

	if response.Status.State != ipc.StateConnected {
		t.Errorf("state = %q, want %q", response.Status.State, ipc.StateConnected)
	}
	if response.Status.LocalIP != "10.110.248.16" {
		t.Errorf("local address = %q", response.Status.LocalIP)
	}
	if response.Status.Interface != "svpn0" {
		t.Errorf("interface = %q", response.Status.Interface)
	}
	if response.Status.BytesIn != 100 || response.Status.BytesOut != 200 {
		t.Errorf("counters = %d/%d, want 100/200", response.Status.BytesIn, response.Status.BytesOut)
	}
}

func TestConnectRefusesWithoutACookie(t *testing.T) {
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		t.Fatal("the daemon tried to connect without a cookie")
		return nil, nil
	})

	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, ""))
	if response.OK {
		t.Fatal("expected the request to be refused")
	}
}

func TestConnectRefusesASecondConnection(t *testing.T) {
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return newFakeConnection(), nil
	})

	if response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c")); !response.OK {
		t.Fatalf("first connect failed: %s", response.Error)
	}

	// A second tunnel would fight the first over routes and the interface.
	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))
	if response.OK {
		t.Fatal("expected the second connect to be refused")
	}
}

func TestConnectFailureIsReportedAndRecoverable(t *testing.T) {
	failing := true
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		if failing {
			return nil, errors.New("gateway refused the cookie")
		}
		return newFakeConnection(), nil
	})

	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))
	if response.OK {
		t.Fatal("expected the connect to fail")
	}
	if response.Status.State != ipc.StateError {
		t.Errorf("state = %q, want %q", response.Status.State, ipc.StateError)
	}
	if response.Status.LastError == "" {
		t.Error("the failure was not reported in the status")
	}

	// A failed attempt must not wedge the daemon.
	failing = false
	if response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c")); !response.OK {
		t.Fatalf("retry failed: %s", response.Error)
	}
}

func TestDisconnectClosesTheConnection(t *testing.T) {
	connection := newFakeConnection()
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return connection, nil
	})

	d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))

	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandDisconnect, ""))
	if !response.OK {
		t.Fatalf("disconnect failed: %s", response.Error)
	}
	if !connection.closedFlag() {
		t.Error("the connection was not closed, so the network configuration was left in place")
	}
	if response.Status.State != ipc.StateDisconnected {
		t.Errorf("state = %q, want %q", response.Status.State, ipc.StateDisconnected)
	}
}

func TestADroppedLinkIsReflectedInTheStatus(t *testing.T) {
	connection := newFakeConnection()
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return connection, nil
	})

	d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))

	// A link can die without anyone asking; the daemon must notice on its own
	// rather than reporting connected until the next command.
	connection.drop(errors.New("reading from the tunnel: connection reset"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandStatus, ""))
		if response.Status.State == ipc.StateError {
			if response.Status.LastError == "" {
				t.Error("the reason for the drop was not reported")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the daemon still reports the connection as live after it dropped")
}

func TestShutdownClosesALiveConnection(t *testing.T) {
	connection := newFakeConnection()
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return connection, nil
	})

	d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))

	if err := d.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !connection.closedFlag() {
		t.Error("shutdown left the tunnel configured")
	}
}

func TestUnknownCommandIsRejected(t *testing.T) {
	d := newTestDaemon(t, nil)

	if response := d.Handle(context.Background(), ipc.Peer{}, request("explode", "")); response.OK {
		t.Fatal("expected an unknown command to be refused")
	}
}

func TestShutdownWithNothingConnectedIsNotAnError(t *testing.T) {
	d := newTestDaemon(t, nil)

	// The daemon usually stops with no tunnel up; reporting that as a failure
	// puts a spurious error in the logs on every clean shutdown.
	if err := d.Shutdown(); err != nil {
		t.Fatalf("Shutdown reported an error with nothing connected: %v", err)
	}
}

func TestDisconnectWithNothingConnectedReportsIt(t *testing.T) {
	d := newTestDaemon(t, nil)

	// A user asking to disconnect when nothing is up should still be told.
	response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandDisconnect, ""))
	if response.OK {
		t.Fatal("expected disconnect to report that nothing is connected")
	}
}

func TestADroppedLinkIsTornDown(t *testing.T) {
	connection := newFakeConnection()
	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		return connection, nil
	})

	d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))

	// A link that dies on its own still has an interface and routes behind it.
	// Leaving them in place sends the machine's traffic into a tunnel that no
	// longer carries anything.
	connection.drop(errors.New("connection reset"))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if connection.closedFlag() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("the dropped connection was never closed, so the network configuration was left applied")
}

// TestDisconnectDuringConnectStopsTheAttempt covers an impatient user: while a
// connect is still running there is no connection to close, and reporting
// success without stopping it brought the VPN up moments after they asked for
// the opposite.
func TestDisconnectDuringConnectStopsTheAttempt(t *testing.T) {
	started := make(chan struct{})

	d := newTestDaemon(t, func(ctx context.Context, _ vpn.Options) (Connection, error) {
		close(started)
		<-ctx.Done()

		return nil, ctx.Err()
	})

	connecting := make(chan ipc.Response, 1)
	go func() {
		connecting <- d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))
	}()

	<-started

	if response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandDisconnect, "")); !response.OK {
		t.Fatalf("disconnect during a connect failed: %s", response.Error)
	}

	// A second connect must not slip in while the first is still unwinding.
	if response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c")); response.OK {
		t.Error("a connect was accepted while the previous attempt was still being abandoned")
	}

	select {
	case response := <-connecting:
		if response.OK {
			t.Fatal("the connect reported success after being cancelled")
		}
		if !strings.Contains(response.Error, "cancelled by a disconnect") {
			t.Errorf("connect error = %q, want it to name the disconnect", response.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the disconnect did not stop the connection attempt")
	}

	if state := d.status().State; state != ipc.StateDisconnected {
		t.Errorf("state = %s, want disconnected", state)
	}
}

// TestDisconnectDuringConnectUndoesAConnectionThatWonTheRace covers the other
// side of the same window: cancellation does not always arrive in time, and a
// connection that completed anyway has to be taken down rather than left up
// after the user asked to disconnect.
func TestDisconnectDuringConnectUndoesAConnectionThatWonTheRace(t *testing.T) {
	connection := newFakeConnection()
	started := make(chan struct{})
	release := make(chan struct{})

	d := newTestDaemon(t, func(context.Context, vpn.Options) (Connection, error) {
		close(started)
		<-release

		// Ignores the cancellation on purpose: this attempt finished before the
		// disconnect reached it.
		return connection, nil
	})

	connecting := make(chan ipc.Response, 1)
	go func() {
		connecting <- d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandConnect, "c"))
	}()

	<-started

	if response := d.Handle(context.Background(), ipc.Peer{}, request(ipc.CommandDisconnect, "")); !response.OK {
		t.Fatalf("disconnect during a connect failed: %s", response.Error)
	}
	close(release)

	select {
	case response := <-connecting:
		if response.OK {
			t.Fatal("the connect reported success after a disconnect had been asked for")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the connect never finished")
	}

	if !connection.closedFlag() {
		t.Error("the connection that won the race was left up after a disconnect")
	}
	if state := d.status().State; state != ipc.StateDisconnected {
		t.Errorf("state = %s, want disconnected", state)
	}
}
