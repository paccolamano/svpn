// Package log renders diagnostics for the two audiences this project has.
//
// A person running `svpn up` is watching it happen: they want a sentence, on
// stderr, with the program's name in front of it and nothing else. A timestamp
// tells them what they already know, a source file tells them about a machine
// that is not theirs, and a log level on an ordinary line is noise — which is
// why every established command-line tool prints "svpn: error: …" and stops
// there.
//
// journald is watching svpnd, and wants the opposite: it records the time, the
// unit and the pid itself, so repeating them is duplication. What it cannot
// work out on its own is the severity, and it will not read a level out of the
// message text: it reads a "<N>" syslog prefix, and without one every line
// svpnd writes is filed as PRIORITY=6 and "journalctl -p err -u svpnd" finds
// nothing.
//
// Nothing here is coloured. Escape codes have to be suppressed for a pipe, a
// file and the journal, which is every destination but one, and what they buy
// on that one is a level word that is already a word.
//
// So the format is chosen by where the output goes, not by which binary is
// running — svpnd under systemd and `svpnd install` in a terminal are two
// different audiences for the same program. See isJournal for how the first
// case is recognised, and why the obvious test for it is wrong.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sorintlab/errors"
)

var detailedErrors = &atomic.Bool{}

// SetDetailedErrors turns the stack traces carried by github.com/sorintlab/errors
// into part of every logged error. It is what --detailed-errors switches on.
func SetDetailedErrors(enabled bool) {
	detailedErrors.Store(enabled)
}

// Failure reports the error that ended a command.
//
// The message is the error itself rather than a label with the error hanging
// off it as an attribute: "svpn: error: the daemon is not reachable" is the
// whole of what a reader needs, and the alternative renders as the word error
// three times over.
func Failure(err error) {
	if !detailedErrors.Load() {
		slog.Error(err.Error())

		return
	}

	// A stack is many lines, and the handler indents a multi-line value under
	// the message rather than trying to fit it into key=value.
	slog.Error(err.Error(), slog.String("stack", strings.Join(errors.PrintErrorDetails(err), "\n")))
}

// hintKey is rendered on a line of its own, the way git prints "hint:".
//
// It is the one attribute that is not context for the message but a separate
// instruction to the reader, and burying "run newgrp svpn" at the end of a run
// of key=value pairs is how it gets missed.
const hintKey = "hint"

// NewLogger returns the logger for a program, and the level to raise for
// --debug.
//
// name is the program's own name, printed in front of every line so that its
// output stays identifiable once it is mixed with something else's.
func NewLogger(name string) (*slog.Logger, *slog.LevelVar) {
	level := &slog.LevelVar{}

	return slog.New(newHandler(os.Stderr, name, level)), level
}

// handler renders records for one destination.
type handler struct {
	// mu is shared with every handler derived by WithAttrs and WithGroup, so
	// that concurrent records cannot interleave halfway through a line.
	mu    *sync.Mutex
	out   io.Writer
	name  string
	level slog.Leveler
	// journal reports that stderr is the systemd journal.
	journal bool

	attrs  []slog.Attr
	groups []string
}

func newHandler(out io.Writer, name string, level slog.Leveler) *handler {
	return &handler{
		mu:      &sync.Mutex{},
		out:     out,
		name:    name,
		level:   level,
		journal: isJournal(out),
	}
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	derived := *h
	derived.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	derived.attrs = append(derived.attrs, h.attrs...)
	for _, attr := range attrs {
		derived.attrs = append(derived.attrs, h.qualify(attr))
	}

	return &derived
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	derived := *h
	derived.groups = append(append([]string{}, h.groups...), name)

	return &derived
}

// qualify prefixes an attribute's key with the groups it was opened under.
func (h *handler) qualify(attr slog.Attr) slog.Attr {
	if len(h.groups) == 0 {
		return attr
	}

	attr.Key = strings.Join(h.groups, ".") + "." + attr.Key

	return attr
}

func (h *handler) Handle(_ context.Context, record slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.attrs)+record.NumAttrs())
	attrs = append(attrs, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, h.qualify(attr))

		return true
	})

	var line strings.Builder
	if h.journal {
		h.renderJournal(&line, record, attrs)
	} else {
		h.renderHuman(&line, record, attrs)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := io.WriteString(h.out, line.String())

	return errors.Wrapf(err, "writing a log record")
}

// renderHuman writes the form a person reads:
//
//	svpn: error: the daemon is not reachable socket=/run/svpn/sock
//	svpn: hint: check the service is running
func (h *handler) renderHuman(line *strings.Builder, record slog.Record, attrs []slog.Attr) {
	// The time is the one thing --debug adds back. Someone reading a live
	// command already knows when it is; someone reading a debug trace is
	// working out what took so long.
	timestamp := ""
	if h.level.Level() <= slog.LevelDebug {
		timestamp = record.Time.Format("15:04:05.000") + " "
	}

	prefix := timestamp + h.name + ": "

	line.WriteString(prefix)
	if label := levelLabel(record.Level); label != "" {
		line.WriteString(label + ": ")
	}
	line.WriteString(record.Message)

	var hint string
	var blocks []slog.Attr
	for _, attr := range attrs {
		value := attr.Value.String()
		switch {
		case attr.Key == hintKey:
			hint = value
		case strings.Contains(value, "\n"):
			blocks = append(blocks, attr)
		default:
			line.WriteString(" " + attr.Key + "=" + value)
		}
	}
	line.WriteString("\n")

	if hint != "" {
		line.WriteString(prefix + "hint: " + hint + "\n")
	}

	// A stack, or anything else that arrived with newlines in it, is indented
	// under the message instead of being crammed into a key=value pair.
	for _, attr := range blocks {
		for _, blockLine := range strings.Split(strings.TrimRight(attr.Value.String(), "\n"), "\n") {
			line.WriteString("    " + blockLine + "\n")
		}
	}
}

// renderJournal writes the form systemd reads:
//
//	<3>the daemon is not reachable socket=/run/svpn/sock
//
// The "<N>" is a syslog priority. systemd strips it and files the entry under
// it, which is the only way a level set here survives to "journalctl -p".
func (h *handler) renderJournal(line *strings.Builder, record slog.Record, attrs []slog.Attr) {
	fmt.Fprintf(line, "<%d>%s", priority(record.Level), flatten(record.Message))

	for _, attr := range attrs {
		value := flatten(attr.Value.String())
		if strings.ContainsAny(value, " \"") {
			value = fmt.Sprintf("%q", value)
		}
		fmt.Fprintf(line, " %s=%s", attr.Key, value)
	}

	line.WriteString("\n")
}

// flatten folds a value onto one line.
//
// journald splits on newlines, so a stack trace written raw becomes a dozen
// separate entries, all but the first without the priority prefix and so all
// but the first filed as info.
func flatten(value string) string {
	if !strings.ContainsAny(value, "\n\r") {
		return value
	}

	return strings.Join(strings.Fields(value), " ")
}

// priority maps a level onto the syslog severities systemd understands.
func priority(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 3 // err
	case level >= slog.LevelWarn:
		return 4 // warning
	case level >= slog.LevelInfo:
		return 6 // info
	default:
		return 7 // debug
	}
}

// levelLabel names a level for a human, and says nothing at all for info.
//
// "svpn: info: connected" is how a log file talks, not how a command does.
func levelLabel(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warning"
	case level >= slog.LevelInfo:
		return ""
	default:
		return "debug"
	}
}

var _ slog.Handler = (*handler)(nil)
