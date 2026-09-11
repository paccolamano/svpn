//go:build !windows

package ipc

import (
	"os"
	"os/user"
	"slices"
	"strconv"
	"syscall"
)

// PendingGroup names the group that owns the socket when the caller belongs to
// it on disk but not in this session, and reports whether that is the case.
//
// This is the single most common way a fresh installation looks broken.
// usermod adds the account to the group in /etc/group, but a process inherits
// its groups from the session that started it, and getgroups() was fixed at
// login. So the socket refuses the very user who was just given access, and
// "permission denied" points at the wrong thing entirely — the fix is newgrp
// or a new login, not a change to any permission.
func PendingGroup(socket string) (string, bool) {
	info, err := os.Stat(socket)
	if err != nil {
		return "", false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	owner := int(stat.Gid)

	// Already effective in this process: whatever went wrong, it is not this.
	current, err := os.Getgroups()
	if err != nil {
		return "", false
	}
	if slices.Contains(current, owner) {
		return "", false
	}

	account, err := user.Current()
	if err != nil {
		return "", false
	}

	ids, err := account.GroupIds()
	if err != nil {
		return "", false
	}
	if !slices.Contains(ids, strconv.Itoa(owner)) {
		// Not a member at all, so this is an account that was never given
		// access rather than a session that predates being given it.
		return "", false
	}

	group, err := user.LookupGroupId(strconv.Itoa(owner))
	if err != nil {
		return strconv.Itoa(owner), true
	}

	return group.Name, true
}
