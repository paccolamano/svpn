// Package ui draws the desktop client.
//
// Nothing here decides anything: it renders a state.Snapshot and turns clicks
// into calls on a state.Controller. Keeping the logic on the other side of that
// line is what lets it be tested on a machine with no display.
package gui

import (
	"fmt"
	"strings"
	"time"

	"github.com/paccolamano/svpn/internal/client/state"
)

// absent is what a field reads as before there is anything to put in it. A
// blank cell looks like a rendering failure; a dash looks like an answer.
const absent = "—"

// headline names a phase in the words a user would use. "Authenticating" is
// deliberately about the browser rather than about SAML: the window is asking
// someone to go and do something, not reporting a protocol step.
func headline(phase state.Phase) string {
	switch phase {
	case state.PhaseConnected:
		return "Connected"
	case state.PhaseConnecting:
		return "Connecting…"
	case state.PhaseDisconnecting:
		return "Disconnecting…"
	case state.PhaseAuthenticating:
		return "Waiting for your browser…"
	case state.PhaseDisconnected:
		return "Not connected"
	case state.PhaseNoDaemon:
		// Short on purpose: the headline is a canvas text, so its width feeds
		// straight into the window's minimum. A sentence here made the card
		// permanently wider than every other state needs it to be. The hint
		// underneath carries the detail.
		return "Service not reachable"
	case state.PhaseUnknown:
		return "Checking…"
	default:
		return "Checking…"
	}
}

// hint is the second line under the headline: what to do about the phase, when
// there is something to do. PhaseNoDaemon is the one that matters — on a fresh
// machine it is the first thing anyone sees, and "not reachable" on its own
// gives them nowhere to go.
func hint(snapshot state.Snapshot) string {
	switch snapshot.Phase {
	case state.PhaseNoDaemon:
		return "Run `sudo svpnd install`, then make sure you are in the svpn group."
	case state.PhaseAuthenticating:
		return "Finish signing in; this window is waiting for the callback."
	case state.PhaseConnected, state.PhaseConnecting, state.PhaseDisconnecting,
		state.PhaseDisconnected, state.PhaseUnknown:
		return ""
	default:
		return ""
	}
}

// formatBytes renders a byte count the way a transfer readout should: three
// significant figures at most, because the digit that changes ten times a
// second carries no information a person can use.
func formatBytes(count int64) string {
	if count < 0 {
		return absent
	}

	const unit = 1024

	if count < unit {
		return fmt.Sprintf("%d B", count)
	}

	value := float64(count)
	units := []string{"kB", "MB", "GB", "TB", "PB"}

	var suffix string

	for _, name := range units {
		value /= unit
		suffix = name

		if value < unit {
			break
		}
	}

	if value >= 100 {
		return fmt.Sprintf("%.0f %s", value, suffix)
	}

	return fmt.Sprintf("%.1f %s", value, suffix)
}

// formatDuration renders an uptime as hh:mm:ss, which stays the same width as
// it counts up. A label that changes width makes the whole row twitch.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}

	total := int(d.Seconds())

	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total/60)%60, total%60)
}

// formatSince renders how long ago a moment was, or absent for the zero time,
// which is what the daemon sends when there is nothing to time.
func formatSince(since time.Time, now time.Time) string {
	if since.IsZero() {
		return absent
	}

	return formatDuration(now.Sub(since))
}

// formatList joins values, naming how many were left out rather than silently
// truncating: a DNS list that shows two of five servers and says so is honest,
// one that just stops is not.
func formatList(values []string, limit int) string {
	if len(values) == 0 {
		return absent
	}

	if limit <= 0 || len(values) <= limit {
		return strings.Join(values, ", ")
	}

	return fmt.Sprintf("%s (+%d more)", strings.Join(values[:limit], ", "), len(values)-limit)
}

// whileConnected returns a figure only when there is a tunnel to describe.
//
// The daemon stamps Status.Since on every state transition, not on the moment
// the tunnel came up — see d.since in internal/daemon — so a disconnected
// status still carries a recent timestamp. Rendering it unconditionally had
// the window report an uptime of eleven seconds directly under the words "Not
// connected". The byte counters are gated with it for the same reason: they
// describe a connection, and there isn't one.
func whileConnected(connected bool, value string) string {
	if !connected {
		return absent
	}

	return value
}

// orAbsent keeps an empty field from rendering as a blank cell.
func orAbsent(value string) string {
	if strings.TrimSpace(value) == "" {
		return absent
	}

	return value
}

// formatCount renders a plain count with its noun, singular when it should be.
func formatCount(count int) string {
	if count == 1 {
		return "1 route"
	}

	return fmt.Sprintf("%d routes", count)
}
