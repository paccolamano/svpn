//go:build !windows

package log

import (
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// isJournal reports whether out is the journal stream systemd opened for this
// service.
//
// The presence of JOURNAL_STREAM is not the answer, and taking it as one is
// the trap systemd's own documentation warns about: the variable is inherited
// by every descendant, so a shell started from a unit — or anything run out of
// one — carries it while its stderr is an ordinary terminal. What the variable
// holds is the device and inode of the real journal stream, precisely so that
// a process can compare them against its own and tell the difference. Reading
// it alone made svpn print "<3>" at an interactive prompt.
func isJournal(out io.Writer) bool {
	stream := os.Getenv("JOURNAL_STREAM")
	if stream == "" {
		return false
	}

	rawDevice, rawInode, ok := strings.Cut(stream, ":")
	if !ok {
		return false
	}

	device, err := strconv.ParseUint(rawDevice, 10, 64)
	if err != nil {
		return false
	}
	inode, err := strconv.ParseUint(rawInode, 10, 64)
	if err != nil {
		return false
	}

	ourDevice, ourInode, ok := streamIdentity(out)
	if !ok {
		return false
	}

	return ourDevice == device && ourInode == inode
}

// streamIdentity reports the device and inode behind a writer, which is how
// JOURNAL_STREAM names the stream it refers to.
func streamIdentity(out io.Writer) (device, inode uint64, ok bool) {
	file, isFile := out.(*os.File)
	if !isFile {
		return 0, 0, false
	}

	info, err := file.Stat()
	if err != nil {
		return 0, 0, false
	}

	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}

	// These fields have different widths and signedness per platform — Dev is
	// int32 on macOS and uint64 on Linux — so the conversions are redundant on
	// the host that lints and load-bearing on the one that does not. A device
	// number is never negative, so widening is safe either way.
	return uint64(stat.Dev), uint64(stat.Ino), true //nolint:unconvert
}
