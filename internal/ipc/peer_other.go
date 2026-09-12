//go:build !linux

package ipc

import (
	"net"

	"github.com/sorintlab/errors"
)

// peerCredentials is unimplemented outside Linux.
//
// macOS offers LOCAL_PEERCRED and Windows exposes the client token over a
// named pipe; both are worth adding before the daemon runs there. Until then
// Peer.Known stays false, so any policy that depends on the caller's identity
// will refuse rather than guess.
func peerCredentials(net.Conn) (Peer, error) {
	return Peer{}, errors.New("peer credentials are not implemented on this platform")
}
