//go:build windows

package ipc

import (
	"net"

	"github.com/sorintlab/errors"
)

// DefaultSocket is the named pipe the daemon would listen on.
const DefaultSocket = `\\.\pipe\svpn`

// errUnsupported explains why the Windows transport is absent rather than
// broken: named pipes need either github.com/Microsoft/go-winio or direct
// syscalls, and the access control that matters there is a security descriptor
// on the pipe rather than file permissions. Adding it is deliberate work, not
// a port of the code below.
var errUnsupported = errors.New("the daemon's IPC transport is not implemented on Windows yet")

// Listen is not implemented on Windows.
func Listen(string, string) (net.Listener, error) { return nil, errUnsupported }

// Dial is not implemented on Windows.
func Dial(string) (net.Conn, error) { return nil, errUnsupported }

// PendingGroup has no meaning on Windows, where access to a named pipe is a
// security descriptor rather than a group that a session has to be restarted
// to pick up.
func PendingGroup(string) (string, bool) { return "", false }
