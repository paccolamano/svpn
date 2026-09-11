package log

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// render runs one record through a handler writing to a buffer, which is never
// a terminal, which nothing here depends on any more.
func render(t *testing.T, journal bool, level slog.Level, record slog.Record) string {
	t.Helper()

	var buffer bytes.Buffer

	leveller := &slog.LevelVar{}
	leveller.Set(level)

	handler := newHandler(&buffer, "svpn", leveller)
	handler.journal = journal

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	return buffer.String()
}

func recordWith(level slog.Level, message string, attrs ...slog.Attr) slog.Record {
	record := slog.NewRecord(time.Date(2026, 9, 10, 18, 16, 11, 0, time.UTC), level, message, 0)
	record.AddAttrs(attrs...)

	return record
}

func TestHumanInfoIsJustTheSentence(t *testing.T) {
	got := render(t, false, slog.LevelInfo, recordWith(slog.LevelInfo, "interrupted"))

	// "svpn: info: interrupted" is how a log file talks. A command says what
	// happened and nothing else.
	if got != "svpn: interrupted\n" {
		t.Errorf("got %q, want %q", got, "svpn: interrupted\n")
	}
}

func TestHumanLabelsOnlyWarningsAndErrors(t *testing.T) {
	tests := []struct {
		level slog.Level
		want  string
	}{
		{level: slog.LevelWarn, want: "svpn: warning: careful\n"},
		{level: slog.LevelError, want: "svpn: error: careful\n"},
	}

	for _, test := range tests {
		if got := render(t, false, slog.LevelInfo, recordWith(test.level, "careful")); got != test.want {
			t.Errorf("got %q, want %q", got, test.want)
		}
	}
}

func TestHumanCarriesNoTimestampUntilDebug(t *testing.T) {
	// Someone watching a command run already knows what time it is; someone
	// reading a debug trace is working out what took so long.
	plain := render(t, false, slog.LevelInfo, recordWith(slog.LevelInfo, "connected"))
	if strings.Contains(plain, "18:16:11") {
		t.Errorf("got %q, want no timestamp at info", plain)
	}

	debug := render(t, false, slog.LevelDebug, recordWith(slog.LevelInfo, "connected"))
	if !strings.Contains(debug, "18:16:11") {
		t.Errorf("got %q, want a timestamp at debug", debug)
	}
}

func TestHumanPutsAHintOnItsOwnLine(t *testing.T) {
	got := render(t, false, slog.LevelInfo, recordWith(slog.LevelWarn,
		"the client and the daemon are different builds",
		slog.String("client", "v1.0.0"),
		slog.String(hintKey, "run \"sudo svpnd update\""),
	))

	want := "svpn: warning: the client and the daemon are different builds client=v1.0.0\n" +
		"svpn: hint: run \"sudo svpnd update\"\n"
	// A hint is an instruction, not context. At the end of a run of key=value
	// pairs it is exactly what gets skipped.
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHumanIndentsAMultiLineValue(t *testing.T) {
	got := render(t, false, slog.LevelInfo, recordWith(slog.LevelError,
		"could not connect",
		slog.String("stack", "one\ntwo"),
	))

	want := "svpn: error: could not connect\n    one\n    two\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOutputIsNeverColoured(t *testing.T) {
	// A terminal is one destination out of several, and escape codes written to
	// a pipe, a file or the journal are not decoration but corruption that
	// outlives the run. So nothing here emits them, anywhere.
	for _, journal := range []bool{false, true} {
		got := render(t, journal, slog.LevelDebug, recordWith(slog.LevelError, "boom",
			slog.String("key", "value"),
			slog.String(hintKey, "do the thing"),
		))
		if strings.Contains(got, "\x1b") {
			t.Errorf("got %q, want no escape codes (journal=%v)", got, journal)
		}
	}
}

func TestJournalCarriesThePriorityPrefix(t *testing.T) {
	tests := []struct {
		level slog.Level
		want  string
	}{
		{level: slog.LevelError, want: "<3>"},
		{level: slog.LevelWarn, want: "<4>"},
		{level: slog.LevelInfo, want: "<6>"},
		{level: slog.LevelDebug, want: "<7>"},
	}

	for _, test := range tests {
		got := render(t, true, slog.LevelDebug, recordWith(test.level, "listening"))
		// Without this prefix journald files every line as PRIORITY=6 and
		// "journalctl -p err -u svpnd" never matches anything.
		if !strings.HasPrefix(got, test.want) {
			t.Errorf("got %q, want it to start with %q", got, test.want)
		}
	}
}

func TestJournalOmitsWhatJournaldAlreadyRecords(t *testing.T) {
	got := render(t, true, slog.LevelDebug, recordWith(slog.LevelInfo, "listening",
		slog.String("socket", "/run/svpn/sock"),
	))

	want := "<6>listening socket=/run/svpn/sock\n"
	// journald stamps the time, the unit and the pid itself, and the program
	// name is already in the "svpnd[pid]:" it prints.
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestJournalFoldsANewlineOntoOneLine(t *testing.T) {
	got := render(t, true, slog.LevelDebug, recordWith(slog.LevelError, "failed",
		slog.String("stack", "one\ntwo"),
	))

	// journald splits on newlines, so a stack written raw becomes several
	// entries and every one after the first loses its priority prefix.
	if strings.Count(got, "\n") != 1 {
		t.Errorf("got %q, want a single line", got)
	}
	if !strings.Contains(got, `stack="one two"`) {
		t.Errorf("got %q, want the value folded and quoted", got)
	}
}

func TestIsJournalIgnoresAnInheritedVariable(t *testing.T) {
	// JOURNAL_STREAM is inherited by every descendant of a unit, so a shell
	// started from one — or anything run out of that shell — carries it while
	// its stderr is an ordinary terminal. Reading the variable alone made the
	// client print "<3>" at a prompt.
	t.Setenv("JOURNAL_STREAM", "9:22888")

	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatalf("creating a stand-in for stderr: %v", err)
	}
	defer func() { _ = file.Close() }()

	if isJournal(file) {
		t.Error("isJournal was fooled by an inherited JOURNAL_STREAM")
	}
}

func TestIsJournalMatchesTheRealStream(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatalf("creating a stand-in for stderr: %v", err)
	}
	defer func() { _ = file.Close() }()

	device, inode, ok := streamIdentity(file)
	if !ok {
		t.Skip("the platform does not expose a device and inode")
	}
	t.Setenv("JOURNAL_STREAM", fmt.Sprintf("%d:%d", device, inode))

	if !isJournal(file) {
		t.Error("isJournal did not recognise a stream naming its own device and inode")
	}
}
