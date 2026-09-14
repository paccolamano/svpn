//go:build linux

package ipc

import (
	"net"

	"github.com/sorintlab/errors"
	"golang.org/x/sys/unix"
)

// peerCredentials reads the identity of the process at the other end.
//
// The kernel supplies these, so unlike anything the client could send they
// cannot be forged. Filesystem permissions on the socket are the first gate;
// this is what lets policy go further than "could open the file".
func peerCredentials(conn net.Conn) (Peer, error) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, errors.Errorf("connection is %T, not a unix socket", conn)
	}

	raw, err := unixConn.SyscallConn()
	if err != nil {
		return Peer{}, errors.Wrapf(err, "accessing the socket")
	}

	var ucred *unix.Ucred
	var credErr error

	if err := raw.Control(func(fd uintptr) {
		ucred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, errors.Wrapf(err, "reading the socket options")
	}
	if credErr != nil {
		return Peer{}, errors.Wrapf(credErr, "reading the peer credentials")
	}

	return Peer{UID: ucred.Uid, GID: ucred.Gid, PID: ucred.Pid, Known: true}, nil
}
