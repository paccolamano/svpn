package ui

import (
	"testing"
	"time"

	"github.com/paccolamano/svpn/gui/internal/core"
)

func TestFormatBytesStaysThreeSignificantFigures(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		999:        "999 B",
		1024:       "1.0 kB",
		1536:       "1.5 kB",
		102400:     "100 kB",
		1048576:    "1.0 MB",
		1503238553: "1.4 GB",
	}

	for count, want := range cases {
		if got := formatBytes(count); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", count, got, want)
		}
	}
}

func TestFormatDurationKeepsItsWidth(t *testing.T) {
	cases := map[time.Duration]string{
		0:                               "00:00:00",
		9 * time.Second:                 "00:00:09",
		12*time.Minute + 43*time.Second: "00:12:43",
		25 * time.Hour:                  "25:00:00",
		-time.Second:                    "00:00:00",
	}

	for d, want := range cases {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestFormatSinceHandlesTheZeroTime(t *testing.T) {
	if got := formatSince(time.Time{}, time.Now()); got != absent {
		t.Errorf("a connection that never started rendered as %q", got)
	}

	now := time.Now()
	if got := formatSince(now.Add(-90*time.Second), now); got != "00:01:30" {
		t.Errorf("formatSince = %q, want 00:01:30", got)
	}
}

func TestFormatListSaysWhatItLeftOut(t *testing.T) {
	if got := formatList(nil, 2); got != absent {
		t.Errorf("an empty list rendered as %q", got)
	}

	if got := formatList([]string{"10.0.0.1", "10.0.0.2"}, 2); got != "10.0.0.1, 10.0.0.2" {
		t.Errorf("formatList = %q", got)
	}

	// Truncating silently would let a window claim the tunnel has two DNS
	// servers when it has four.
	if got := formatList([]string{"a", "b", "c", "d"}, 2); got != "a, b (+2 more)" {
		t.Errorf("formatList = %q, want the count of what was hidden", got)
	}
}

// TestEveryPhaseHasWords guards the switch statements: a phase added to core
// and forgotten here renders as the default, which is a window that says
// "Checking…" forever with no way to tell that anything is wrong.
func TestEveryPhaseHasWords(t *testing.T) {
	phases := []core.Phase{
		core.PhaseUnknown,
		core.PhaseNoDaemon,
		core.PhaseDisconnected,
		core.PhaseAuthenticating,
		core.PhaseConnecting,
		core.PhaseConnected,
		core.PhaseDisconnecting,
	}

	seen := make(map[string]core.Phase, len(phases))

	for _, phase := range phases {
		words := headline(phase)
		if words == "" {
			t.Errorf("phase %q has no headline", phase)
		}

		if other, clash := seen[words]; clash && phase != core.PhaseUnknown {
			t.Errorf("phases %q and %q both render as %q", phase, other, words)
		}

		seen[words] = phase

		if statusColorName(phase) == "" {
			t.Errorf("phase %q has no status colour", phase)
		}
	}
}

func TestOrAbsentTreatsBlanksAsEmpty(t *testing.T) {
	if got := orAbsent("   "); got != absent {
		t.Errorf("orAbsent(spaces) = %q", got)
	}

	if got := orAbsent("svpn0"); got != "svpn0" {
		t.Errorf("orAbsent = %q", got)
	}
}

func TestCountOrAbsentIsSingularForOne(t *testing.T) {
	if got := countOrAbsent(0); got != absent {
		t.Errorf("countOrAbsent(0) = %q", got)
	}

	if got := countOrAbsent(1); got != "1 route" {
		t.Errorf("countOrAbsent(1) = %q", got)
	}

	if got := countOrAbsent(178); got != "178 routes" {
		t.Errorf("countOrAbsent(178) = %q", got)
	}
}

// TestFiguresAreBlankWithoutATunnel guards the one that actually shipped
// wrong: Status.Since is when the current state began, so a disconnected
// daemon reports a timestamp from seconds ago and the window claimed an uptime
// under the words "Not connected".
func TestFiguresAreBlankWithoutATunnel(t *testing.T) {
	if got := whileConnected(false, "00:00:11"); got != absent {
		t.Errorf("a disconnected tunnel reported an uptime of %q", got)
	}

	if got := whileConnected(true, "00:00:11"); got != "00:00:11" {
		t.Errorf("whileConnected(true) = %q", got)
	}
}
