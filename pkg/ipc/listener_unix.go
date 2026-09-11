//go:build !windows

package ipc

import (
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/sorintlab/errors"
)

// DefaultSocket is where the daemon listens.
const DefaultSocket = "/run/svpn/sock"

// maxSocketPath is how much of a path fits in sockaddr_un.sun_path, minus the
// terminating NUL. Exceeding it makes bind fail with EINVAL, which says
// nothing about the real cause, so it is checked up front.
const maxSocketPath = 107

// Listen creates the daemon's socket.
//
// The socket is the first access-control gate, so it is created with mode 0660
// under a directory owned by the daemon: only root and members of the socket's
// group can reach it at all. Whether a peer that gets through may act is then
// decided by Server.Authorize.
//
// group, when set, is handed the socket so ordinary users in it can reach the
// daemon. Without it a daemon running as root produces a root-owned socket
// that no unprivileged client can open, which defeats the split this design
// exists for. It accepts a group name or a numeric gid.
//
// The socket stays root-owned. Handing it to a user instead would spare them
// the fresh login a new group membership needs, but chowning to another uid
// takes CAP_CHOWN, and the unit drops every capability but CAP_NET_ADMIN and
// CAP_NET_RAW. Changing the group is exempt because the daemon runs with
// Group=svpn and so is already a member.
func Listen(path, group string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, errors.Errorf(
			"socket path is %d bytes, over the %d byte limit for unix sockets: %s",
			len(path), maxSocketPath, path)
	}

	// 0755, not 0750: the directory has to be traversable by every client, and
	// it holds nothing but the socket, whose own 0660 is the actual gate.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec
		return nil, errors.Wrapf(err, "creating the socket directory")
	}

	// A socket left behind by a crash would make Listen fail; removing a stale
	// one is safe because a live daemon holds a lock on the path by virtue of
	// listening, and a second Listen on a live socket still fails below.
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}

	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, errors.Wrapf(err, "listening on %s", path)
	}

	// 0660 is the point of the group handed the socket below: root owns it,
	// members of that group may open it, nobody else can.
	if err := os.Chmod(path, 0o660); err != nil { //nolint:gosec
		_ = listener.Close()
		return nil, errors.Wrapf(err, "setting the socket permissions")
	}

	if group != "" {
		gid, err := lookupGID(group)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
		// -1 leaves the owner untouched; only the group changes, which needs no
		// CAP_CHOWN because the daemon is already a member of that group.
		if err := os.Chown(path, -1, gid); err != nil {
			_ = listener.Close()
			return nil, errors.Wrapf(err, "giving the socket to group %s", group)
		}
	}

	return listener, nil
}

// lookupGID resolves a group name or numeric id.
func lookupGID(group string) (int, error) {
	if gid, err := strconv.Atoi(group); err == nil {
		return gid, nil
	}

	found, err := user.LookupGroup(group)
	if err != nil {
		// The group not existing yet is the normal first-run failure, and
		// "unknown group" alone does not suggest the fix. Refusing is still
		// right: skipping the chown would leave a root-owned socket that no
		// client could ever open, which fails later and less clearly.
		var unknown user.UnknownGroupError
		if errors.As(err, &unknown) {
			return 0, errors.Errorf(
				"group %q does not exist; create it with \"groupadd %s\" and add the users who may control the VPN, or pass --socket-group with a group that already exists",
				group, group)
		}
		return 0, errors.Wrapf(err, "looking up group %q", group)
	}

	gid, err := strconv.Atoi(found.Gid)
	if err != nil {
		return 0, errors.Wrapf(err, "group %q has a non-numeric id %q", group, found.Gid)
	}

	return gid, nil
}

// removeStaleSocket deletes a socket no daemon is listening on.
func removeStaleSocket(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "inspecting %s", path)
	}

	if info.Mode()&os.ModeSocket == 0 {
		return errors.Errorf("%s exists and is not a socket", path)
	}

	// A successful dial means a daemon is already running; refuse rather than
	// pulling the socket out from under it.
	if conn, err := net.Dial("unix", path); err == nil {
		_ = conn.Close()
		return errors.Errorf("another daemon is already listening on %s", path)
	}

	if err := os.Remove(path); err != nil {
		return errors.Wrapf(err, "removing the stale socket %s", path)
	}

	return nil
}

// Dial connects to the daemon.
func Dial(path string) (net.Conn, error) {
	if len(path) > maxSocketPath {
		return nil, errors.Errorf(
			"socket path is %d bytes, over the %d byte limit for unix sockets: %s",
			len(path), maxSocketPath, path)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, errors.Wrapf(err, "connecting to the daemon at %s", path)
	}

	return conn, nil
}
