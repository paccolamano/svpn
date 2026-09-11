// Package core holds the half of the desktop client that draws nothing: it
// talks to svpnd, runs the browser login, and reduces both to the single value
// the UI renders.
//
// Keeping Fyne out of here is deliberate. Everything below can be exercised on
// a machine with no display and no CAP_NET_ADMIN, which is the only kind of
// machine this repository's tests ever run on — the same reason internal/vpn
// is driven through seams rather than against a real gateway.
package core

import (
	"github.com/paccolamano/svpn/pkg/ipc"
)

// Phase is what the client is doing, as the user would describe it.
//
// It is not ipc.State, and the extra members are the reason: the browser login
// and an unreachable daemon both happen entirely on this side of the socket,
// so the daemon has no name for either. A UI that switched on ipc.State would
// have to invent them anyway, one widget at a time.
type Phase string

// The phases a session moves through.
const (
	// PhaseUnknown is the moment before the first status reply, when nothing
	// has been established yet — including whether a daemon is there at all.
	PhaseUnknown Phase = "unknown"
	// PhaseNoDaemon means the control socket could not be opened. On a fresh
	// machine this is the first thing the user sees, not an edge case: it is
	// what "svpnd install has not been run" and "you are not in the svpn
	// group yet" both look like from out here.
	PhaseNoDaemon       Phase = "no-daemon"
	PhaseDisconnected   Phase = "disconnected"
	PhaseAuthenticating Phase = "authenticating"
	PhaseConnecting     Phase = "connecting"
	PhaseConnected      Phase = "connected"
	PhaseDisconnecting  Phase = "disconnecting"
)

// Snapshot is the whole of what the UI renders. The controller publishes these
// as complete values rather than emitting events for individual fields: a
// dropped event would leave a widget lying, whereas a dropped snapshot is
// superseded by the next one a second later.
type Snapshot struct {
	Phase Phase
	// Status is the daemon's last reply. It is kept while a local action is in
	// flight so the detail panel does not blank out mid-connect.
	Status ipc.Status
	// LoginURL is where the identity provider is waiting. It is set only while
	// PhaseAuthenticating, and exists because that wait lasts as long as the
	// user takes: without showing the URL, a browser that failed to open
	// leaves the window spinning with nothing to act on.
	LoginURL string
	// Err is the most recent local failure — a refused login, a daemon that
	// would not answer. It survives until the next action starts, so a failure
	// that flips the phase straight back to disconnected still explains
	// itself. Status.LastError is the daemon's own account of a failure and is
	// shown separately.
	Err string
}

// Busy reports that something is in flight and a second command would collide
// with it.
func (s Snapshot) Busy() bool {
	switch s.Phase {
	case PhaseAuthenticating, PhaseConnecting, PhaseDisconnecting:
		return true
	case PhaseUnknown, PhaseNoDaemon, PhaseDisconnected, PhaseConnected:
		return false
	default:
		return false
	}
}

// CanConnect reports whether asking for a connection now would make sense.
func (s Snapshot) CanConnect() bool { return s.Phase == PhaseDisconnected }

// CanDisconnect reports whether there is a connection to take down.
func (s Snapshot) CanDisconnect() bool { return s.Phase == PhaseConnected }

// phaseOf maps the daemon's own state onto a phase.
//
// StateError becomes PhaseDisconnected on purpose: there is no tunnel either
// way, and the daemon keeps the reason in Status.LastError precisely so a
// client that polls can report it after the fact. Giving it a phase of its own
// would mean a window stuck on "error" with no action that clears it.
func phaseOf(state ipc.State) Phase {
	switch state {
	case ipc.StateConnected:
		return PhaseConnected
	case ipc.StateConnecting:
		return PhaseConnecting
	case ipc.StateDisconnecting:
		return PhaseDisconnecting
	case ipc.StateDisconnected, ipc.StateError:
		return PhaseDisconnected
	default:
		return PhaseUnknown
	}
}
