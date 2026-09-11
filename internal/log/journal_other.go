//go:build windows

package log

import "io"

// isJournal is always false on Windows, which has no journald. When the daemon
// grows a Windows service it will want the event log, and that is a different
// question from this one.
func isJournal(io.Writer) bool { return false }

// streamIdentity has nothing to report on a platform with no journal stream to
// compare against.
func streamIdentity(io.Writer) (device, inode uint64, ok bool) { return 0, 0, false }
