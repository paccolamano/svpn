package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sorintlab/errors"
)

// startServer runs a server on a temporary socket and returns its path.
func startServer(t *testing.T, handler Handler, authorize func(Peer) error) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sock")

	listener, err := Listen(path, "")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	server := NewServer(handler)
	server.Authorize = authorize

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		if err := server.Serve(ctx, listener); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	return path
}

func TestRequestAndResponseRoundTrip(t *testing.T) {
	path := startServer(t, HandlerFunc(func(_ context.Context, _ Peer, request Request) Response {
		if request.Command != CommandStatus {
			return Response{Error: "unexpected command"}
		}
		return Response{OK: true, Status: &Status{State: StateConnected, LocalIP: "10.0.0.1"}}
	}), nil)

	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(Request{Command: CommandStatus}, 5*time.Second)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if !response.OK || response.Status.LocalIP != "10.0.0.1" {
		t.Errorf("response = %+v", response)
	}
}

// TestPeerCredentialsAreReported checks that the identity comes from the
// kernel. It is what lets policy go beyond "could open the socket", so a
// silent failure here would quietly remove an access-control layer.
func TestPeerCredentialsAreReported(t *testing.T) {
	seen := make(chan Peer, 1)

	path := startServer(t, HandlerFunc(func(_ context.Context, peer Peer, _ Request) Response {
		seen <- peer
		return Response{OK: true}
	}), nil)

	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Do(Request{Command: CommandStatus}, 5*time.Second); err != nil {
		t.Fatalf("Do: %v", err)
	}

	select {
	case peer := <-seen:
		if !peer.Known {
			t.Fatal("the peer credentials were not available")
		}
		if peer.UID != uint32(os.Getuid()) {
			t.Errorf("uid = %d, want %d", peer.UID, os.Getuid())
		}
		if peer.PID != int32(os.Getpid()) {
			t.Errorf("pid = %d, want %d", peer.PID, os.Getpid())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler never ran")
	}
}

func TestAuthorizeRefusesAPeer(t *testing.T) {
	path := startServer(t, HandlerFunc(func(context.Context, Peer, Request) Response {
		t.Error("the handler ran for a peer that should have been refused")
		return Response{OK: true}
	}), func(Peer) error {
		return errors.New("not allowed")
	})

	client, err := NewClient(path)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(Request{Command: CommandStatus}, 5*time.Second)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if response.OK || response.Error == "" {
		t.Errorf("expected a refusal, got %+v", response)
	}
}

func TestSocketIsNotWorldAccessible(t *testing.T) {
	path := startServer(t, HandlerFunc(func(context.Context, Peer, Request) Response {
		return Response{OK: true}
	}), nil)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	// The socket is the first gate: anything world-writable would let every
	// local account drive the VPN.
	if mode := info.Mode().Perm(); mode&0o007 != 0 {
		t.Errorf("socket mode is %o, want no permissions for others", mode)
	}
}

func TestListenRefusesToStealALiveSocket(t *testing.T) {
	path := startServer(t, HandlerFunc(func(context.Context, Peer, Request) Response {
		return Response{OK: true}
	}), nil)

	// A second daemon must not pull the socket out from under a running one.
	if _, err := Listen(path, ""); err == nil {
		t.Fatal("expected Listen to refuse a socket that is already served")
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sock")

	listener, err := Listen(path, "")
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}

	// Go unlinks the socket on Close, so a clean shutdown never leaves one
	// behind. Disabling that reproduces what a crash leaves on disk, which is
	// the case a daemon has to recover from on the next start.
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("listener is %T, not a unix listener", listener)
	}
	unixListener.SetUnlinkOnClose(false)
	_ = listener.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the socket was removed despite SetUnlinkOnClose(false): %v", err)
	}

	second, err := Listen(path, "")
	if err != nil {
		t.Fatalf("Listen did not replace the stale socket: %v", err)
	}
	_ = second.Close()
}

func TestMalformedRequestDoesNotDropTheConnection(t *testing.T) {
	path := startServer(t, HandlerFunc(func(context.Context, Peer, Request) Response {
		return Response{OK: true}
	}), nil)

	conn, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("writing: %v", err)
	}

	buf := make([]byte, 512)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("the server closed the connection instead of reporting the error: %v", err)
	}
	if n == 0 {
		t.Fatal("no error was reported")
	}
}

func TestListenRejectsAnOverlongPath(t *testing.T) {
	// A path past sun_path makes bind fail with EINVAL, which gives an
	// operator nothing to work with. The limit is checked so the message names
	// the real problem.
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 200))

	_, err := Listen(long, "")
	if err == nil {
		t.Fatal("expected Listen to refuse an over-long path")
	}
	if !strings.Contains(err.Error(), "unix sockets") {
		t.Errorf("error does not explain the limit: %v", err)
	}
}

func TestListenHandsTheSocketToAGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")

	// Chowning to a group the process already belongs to needs no privileges,
	// so the real path is exercised rather than mocked.
	group := strconv.Itoa(os.Getgid())

	listener, err := Listen(path, group)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("the platform does not expose the socket's owner")
	}
	if int(stat.Gid) != os.Getgid() {
		t.Errorf("socket gid = %d, want %d", stat.Gid, os.Getgid())
	}
}

func TestListenReportsAnUnknownGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")

	// A typo here would otherwise produce a socket nobody can reach, with no
	// hint as to why.
	if _, err := Listen(path, "no-such-group-here"); err == nil {
		t.Fatal("expected Listen to refuse an unknown group")
	}
}

func TestPendingGroupIsQuietWhenTheGroupIsAlreadyEffective(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sock")

	listener, err := Listen(path, strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	// The socket belongs to a group this process already has, so whatever else
	// might go wrong, a stale session is not it and saying so would misdirect.
	if group, pending := PendingGroup(path); pending {
		t.Errorf("PendingGroup() = %q, true; want it quiet for an effective group", group)
	}
}

func TestPendingGroupIsQuietWithoutASocket(t *testing.T) {
	if group, pending := PendingGroup(filepath.Join(t.TempDir(), "absent")); pending {
		t.Errorf("PendingGroup() = %q, true; want it quiet when there is no socket", group)
	}
}
