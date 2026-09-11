package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/pkg/auth"
	"github.com/paccolamano/svpn/pkg/ipc"
)

// fakeDaemon stands in for svpnd. It is reached from the controller's loop and
// from its workers at the same time, so every field is behind the mutex.
type fakeDaemon struct {
	mu sync.Mutex

	status    ipc.Status
	statusErr error
	// onConnect answers a connect request. Nil accepts it.
	onConnect func(request ipc.Request) (ipc.Response, error)
	requests  []ipc.Request
}

func (d *fakeDaemon) Send(request ipc.Request, _ time.Duration) (ipc.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.requests = append(d.requests, request)

	switch request.Command {
	case ipc.CommandStatus:
		if d.statusErr != nil {
			return ipc.Response{}, d.statusErr
		}

		status := d.status

		return ipc.Response{OK: true, Status: &status}, nil

	case ipc.CommandConnect:
		if d.onConnect != nil {
			return d.onConnect(request)
		}

		d.status = ipc.Status{State: ipc.StateConnected, Interface: "svpn0"}
		status := d.status

		return ipc.Response{OK: true, Status: &status}, nil

	case ipc.CommandDisconnect:
		d.status = ipc.Status{State: ipc.StateDisconnected}
		status := d.status

		return ipc.Response{OK: true, Status: &status}, nil
	}

	return ipc.Response{}, errors.Errorf("unexpected command %q", request.Command)
}

func (d *fakeDaemon) sent() []ipc.Request {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]ipc.Request(nil), d.requests...)
}

// testOptions poll fast enough that a test does not spend its life waiting,
// and keep the timeouts long enough that a slow machine does not fail one.
func testOptions() Options {
	return Options{
		Poll:           5 * time.Millisecond,
		StatusTimeout:  time.Second,
		CommandTimeout: 5 * time.Second,
		LoginTimeout:   5 * time.Second,
	}
}

// start runs a controller for the duration of the test.
func start(t *testing.T, daemon Daemon, login Authenticator) *Controller {
	t.Helper()

	controller := New(daemon, login, testOptions())

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		controller.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-stopped
	})

	return controller
}

// waitFor reads snapshots until one satisfies want.
//
// It waits for a condition rather than asserting on the next snapshot because
// Updates deliberately drops states the reader was too slow for: only the
// state things settle in is guaranteed to be seen.
func waitFor(t *testing.T, controller *Controller, what string, want func(Snapshot) bool) Snapshot {
	t.Helper()

	deadline := time.After(5 * time.Second)

	var last Snapshot

	for {
		select {
		case snapshot := <-controller.Updates():
			last = snapshot
			if want(snapshot) {
				return snapshot
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s; last snapshot was %+v", what, last)
		}
	}
}

// okLogin is an authenticator that succeeds immediately.
func okLogin(ctx context.Context, onURL func(string)) (auth.Session, error) {
	onURL("https://idp.example/saml")

	return auth.Session{Cookie: "cookie", Host: "vpn.example", Port: 443}, ctx.Err()
}

func TestUnreachableDaemonBecomesNoDaemon(t *testing.T) {
	daemon := &fakeDaemon{statusErr: errors.Errorf("dial /run/svpn/sock: no such file")}
	controller := start(t, daemon, okLogin)

	snapshot := waitFor(t, controller, "the no-daemon phase", func(s Snapshot) bool {
		return s.Phase == PhaseNoDaemon
	})

	if snapshot.Err == "" {
		t.Fatal("the reason the socket could not be opened was not reported")
	}

	if snapshot.CanConnect() {
		t.Fatal("connecting was offered with no daemon to connect through")
	}
}

func TestConnectAuthenticatesThenAsksTheDaemon(t *testing.T) {
	daemon := &fakeDaemon{status: ipc.Status{State: ipc.StateDisconnected}}
	controller := start(t, daemon, okLogin)

	waitFor(t, controller, "the initial disconnected phase", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected
	})

	controller.Connect()

	snapshot := waitFor(t, controller, "the connected phase", func(s Snapshot) bool {
		return s.Phase == PhaseConnected
	})

	if snapshot.Status.Interface != "svpn0" {
		t.Fatalf("the daemon's status did not reach the snapshot: %+v", snapshot.Status)
	}

	if snapshot.LoginURL != "" {
		t.Fatal("the login URL outlived the login it was showing")
	}

	var connect ipc.Request

	for _, request := range daemon.sent() {
		if request.Command == ipc.CommandConnect {
			connect = request

			break
		}
	}

	if connect.Command == "" {
		t.Fatal("no connect request reached the daemon")
	}

	if connect.Cookie != "cookie" || connect.Host != "vpn.example" || connect.Port != 443 {
		t.Fatalf("the session from the login did not reach the daemon intact: %+v", connect)
	}
}

func TestLoginURLIsPublishedWhileWaiting(t *testing.T) {
	release := make(chan struct{})

	login := func(ctx context.Context, onURL func(string)) (auth.Session, error) {
		onURL("https://idp.example/saml")

		select {
		case <-release:
		case <-ctx.Done():
			return auth.Session{}, ctx.Err()
		}

		return auth.Session{Cookie: "cookie"}, nil
	}

	controller := start(t, &fakeDaemon{status: ipc.Status{State: ipc.StateDisconnected}}, login)
	controller.Connect()

	waitFor(t, controller, "the login URL", func(s Snapshot) bool {
		return s.Phase == PhaseAuthenticating && s.LoginURL == "https://idp.example/saml"
	})

	close(release)

	waitFor(t, controller, "the connected phase", func(s Snapshot) bool {
		return s.Phase == PhaseConnected
	})
}

func TestFailedLoginIsReportedAndLeavesTheTunnelDown(t *testing.T) {
	login := func(context.Context, func(string)) (auth.Session, error) {
		return auth.Session{}, errors.Errorf("the identity provider refused the assertion")
	}

	controller := start(t, &fakeDaemon{status: ipc.Status{State: ipc.StateDisconnected}}, login)
	controller.Connect()

	snapshot := waitFor(t, controller, "the failure to surface", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected && s.Err != ""
	})

	if snapshot.Err == "" {
		t.Fatal("the login failure was swallowed")
	}
}

func TestRefusedConnectReportsWhatTheDaemonSaid(t *testing.T) {
	daemon := &fakeDaemon{
		status: ipc.Status{State: ipc.StateDisconnected},
		onConnect: func(ipc.Request) (ipc.Response, error) {
			return ipc.Response{OK: false, Error: "the gateway rejected the cookie"}, nil
		},
	}

	controller := start(t, daemon, okLogin)
	controller.Connect()

	snapshot := waitFor(t, controller, "the refusal to surface", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected && s.Err != ""
	})

	if snapshot.Err != "the gateway rejected the cookie" {
		t.Fatalf("the daemon's own words were lost: %q", snapshot.Err)
	}
}

func TestCancelledLoginIsNotReportedAsAFailure(t *testing.T) {
	login := func(ctx context.Context, onURL func(string)) (auth.Session, error) {
		onURL("https://idp.example/saml")
		<-ctx.Done()

		return auth.Session{}, ctx.Err()
	}

	controller := start(t, &fakeDaemon{status: ipc.Status{State: ipc.StateDisconnected}}, login)
	controller.Connect()

	waitFor(t, controller, "the login to start", func(s Snapshot) bool {
		return s.Phase == PhaseAuthenticating
	})

	controller.Cancel()

	snapshot := waitFor(t, controller, "the return to disconnected", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected
	})

	if snapshot.Err != "" {
		t.Fatalf("cancelling reported an error to the user who asked for it: %q", snapshot.Err)
	}
}

// TestStaleWorkerCannotOverwriteANewerAction is the reason the controller
// stamps a generation on everything a worker posts back: a login the user
// cancelled finishes anyway, and its reply arrives after the state it belonged
// to is gone.
func TestStaleWorkerCannotOverwriteANewerAction(t *testing.T) {
	release := make(chan struct{})

	login := func(ctx context.Context, onURL func(string)) (auth.Session, error) {
		onURL("https://idp.example/saml")
		<-release

		return auth.Session{}, errors.Errorf("a failure from the abandoned login")
	}

	controller := start(t, &fakeDaemon{status: ipc.Status{State: ipc.StateDisconnected}}, login)
	controller.Connect()

	waitFor(t, controller, "the login to start", func(s Snapshot) bool {
		return s.Phase == PhaseAuthenticating
	})

	controller.Cancel()

	waitFor(t, controller, "the return to disconnected", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected
	})

	// The abandoned login now finishes and reports its failure.
	close(release)

	// Give the stale reply every chance to land before checking it did not.
	time.Sleep(50 * time.Millisecond)

	controller.Refresh()

	snapshot := waitFor(t, controller, "a fresh snapshot", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected
	})

	if snapshot.Err != "" {
		t.Fatalf("a cancelled login's failure surfaced anyway: %q", snapshot.Err)
	}
}

func TestDisconnectAsksTheDaemon(t *testing.T) {
	daemon := &fakeDaemon{status: ipc.Status{State: ipc.StateConnected, Interface: "svpn0"}}
	controller := start(t, daemon, okLogin)

	waitFor(t, controller, "the connected phase", func(s Snapshot) bool {
		return s.Phase == PhaseConnected
	})

	controller.Disconnect()

	waitFor(t, controller, "the disconnected phase", func(s Snapshot) bool {
		return s.Phase == PhaseDisconnected
	})

	var asked bool

	for _, request := range daemon.sent() {
		if request.Command == ipc.CommandDisconnect {
			asked = true
		}
	}

	if !asked {
		t.Fatal("no disconnect request reached the daemon")
	}
}

func TestPhaseOfMapsTheDaemonStates(t *testing.T) {
	cases := map[ipc.State]Phase{
		ipc.StateConnected:     PhaseConnected,
		ipc.StateConnecting:    PhaseConnecting,
		ipc.StateDisconnecting: PhaseDisconnecting,
		ipc.StateDisconnected:  PhaseDisconnected,
		// An error leaves no tunnel up, and Status.LastError carries the
		// reason; a phase of its own would have no action that clears it.
		ipc.StateError: PhaseDisconnected,
		ipc.State("?"): PhaseUnknown,
	}

	for state, want := range cases {
		if got := phaseOf(state); got != want {
			t.Errorf("phaseOf(%q) = %q, want %q", state, got, want)
		}
	}
}
