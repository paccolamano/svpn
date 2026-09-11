//go:build linux

package install

import (
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/pkg/ipc"
)

// unitName is what systemctl is asked about.
const unitName = UnitName

// systemdMarker exists on a booted systemd system and nowhere else. Checking
// it up front turns "systemctl: command not found" into something that says
// which assumption this installer makes.
const systemdMarker = "/run/systemd/system"

// statusTimeout bounds the one question an installation asks a running daemon.
// It is short because the answer only decides whether to restart, and a daemon
// too busy to answer within it is not one to interrupt anyway.
const statusTimeout = 3 * time.Second

// Apply carries out an installation. Every mutation of the system happens
// here; Plan decided all of it.
func Apply(steps Steps, logf func(format string, args ...any)) error {
	if steps.Service {
		if _, err := os.Stat(systemdMarker); err != nil {
			return errors.Errorf("this does not look like a systemd system (%s is missing); use --no-service to install the files anyway", systemdMarker)
		}
	}

	// The group and the membership come first because they are the steps most
	// likely to be refused — they always need root, whatever the prefix is —
	// and failing before anything has been copied leaves less behind.
	created, err := ensureGroup(steps.Group)
	if err != nil {
		return err
	}
	if created {
		logf("created group %s", steps.Group)
	}

	relogin := false
	if steps.User != "" {
		added, err := addUserToGroup(steps.User, steps.Group)
		if err != nil {
			return err
		}
		if added {
			logf("added %s to group %s", steps.User, steps.Group)
			relogin = true
		}
	} else {
		logf("no unprivileged account could be identified, so none was added to group %s; add one with \"usermod -aG %s <user>\" and pass --allow-uid in %s",
			steps.Group, steps.Group, steps.Conf.Path)
	}

	for _, binary := range steps.Binaries {
		if binary.Same {
			logf("%s is already the installed binary, left alone", binary.To)

			continue
		}
		if err := installBinary(binary); err != nil {
			return err
		}
		logf("installed %s", binary.To)
	}

	for _, file := range []File{steps.Unit, steps.Conf} {
		if err := writeFile(file); err != nil {
			return err
		}
		logf("wrote %s", file.Path)
	}

	if steps.Service {
		if err := startService(steps, logf); err != nil {
			return err
		}
	}

	if relogin {
		// The single most common way for a fresh installation to look broken:
		// the group exists, the user is in it, and the shell that ran the
		// install still is not, because getgroups() was fixed at login. A new
		// terminal window is not enough either — it is forked from a session
		// that predates the group.
		logf("group membership does not reach sessions that were already open, and a new terminal window inherits the same ones: run \"newgrp %s\" in this shell, or log out and back in, before \"svpn up\"",
			steps.Group)
	}

	return nil
}

// ApplyUninstall removes what Apply put in place.
func ApplyUninstall(steps UninstallSteps, logf func(format string, args ...any)) error {
	if steps.Service {
		// disable --now sends SIGTERM, which is what lets the daemon tear the
		// tunnel down and restore the routing table before it exits. Killing
		// it instead would leave the machine routing into a dead interface.
		if err := run("systemctl", "disable", "--now", unitName); err != nil {
			logf("could not stop the service, continuing: %v", err)
		}
	}

	for _, path := range steps.Paths {
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return errors.Wrapf(err, "removing %s", path)
		}
		logf("removed %s", path)
	}

	if steps.Service {
		if err := run("systemctl", "daemon-reload"); err != nil {
			return err
		}
	}

	if steps.Purge && steps.Group != "" {
		// groupdel refuses while the group is someone's primary group or still
		// has members, and neither is a reason to fail an uninstall that has
		// already removed everything else.
		if err := run("groupdel", steps.Group); err != nil {
			logf("left group %s in place: %v", steps.Group, err)
		} else {
			logf("removed group %s", steps.Group)
		}
	}

	return nil
}

// installBinary copies one binary into place.
//
// It writes a temporary file next to the destination and renames it, rather
// than writing the destination directly: overwriting a binary that is running
// fails with ETXTBSY, and an install that replaces a live svpnd is the normal
// case, not the exception. The rename also makes the swap atomic, so a failure
// part way through never leaves a truncated binary behind.
func installBinary(binary BinaryCopy) error {
	if err := ensureDir(filepath.Dir(binary.To)); err != nil {
		return err
	}

	source, err := os.Open(binary.From)
	if err != nil {
		return errors.Wrapf(err, "opening %s", binary.From)
	}
	defer func() { _ = source.Close() }()

	temporary, err := os.CreateTemp(filepath.Dir(binary.To), ".svpn-install-*")
	if err != nil {
		return permissionHint(errors.Wrapf(err, "creating a temporary file in %s", filepath.Dir(binary.To)))
	}
	defer func() { _ = os.Remove(temporary.Name()) }()

	if _, err := io.Copy(temporary, source); err != nil {
		_ = temporary.Close()

		return errors.Wrapf(err, "copying %s", binary.From)
	}

	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()

		return errors.Wrapf(err, "setting the mode of %s", binary.To)
	}

	if err := temporary.Close(); err != nil {
		return errors.Wrapf(err, "closing %s", temporary.Name())
	}

	if err := os.Rename(temporary.Name(), binary.To); err != nil {
		return errors.Wrapf(err, "installing %s", binary.To)
	}

	return nil
}

// writeFile writes one of the configuration files.
func writeFile(file File) error {
	if err := ensureDir(filepath.Dir(file.Path)); err != nil {
		return err
	}

	if err := os.WriteFile(file.Path, []byte(file.Content), file.Mode); err != nil {
		return permissionHint(errors.Wrapf(err, "writing %s", file.Path))
	}

	return nil
}

// ensureDir creates a destination directory.
//
// 0755, not 0750: /usr/local/bin has to be traversable by every user who runs
// svpn, and /etc/svpn holds the flags the daemon starts with, not a secret.
func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec
		return permissionHint(errors.Wrapf(err, "creating %s", path))
	}

	return nil
}

// permissionHint names the fix for the failure that every first run hits.
func permissionHint(err error) error {
	if errors.Is(err, os.ErrPermission) && os.Geteuid() != 0 {
		return errors.Wrapf(err, "this needs root: run it under sudo, or point --prefix, --unit-dir and --conf-dir somewhere writable")
	}

	return err
}

// ensureGroup creates the socket group if it is not there already.
func ensureGroup(group string) (bool, error) {
	// A numeric gid names a group that already exists by definition; there is
	// nothing to create and no name to create it under.
	if _, err := strconv.Atoi(group); err == nil {
		return false, nil
	}

	_, err := user.LookupGroup(group)
	if err == nil {
		return false, nil
	}

	var unknown user.UnknownGroupError
	if !errors.As(err, &unknown) {
		return false, errors.Wrapf(err, "looking up group %q", group)
	}

	// A system group, so it lands below the range ordinary user groups use and
	// does not show up as a login group anywhere.
	if err := run("groupadd", "--system", group); err != nil {
		return false, err
	}

	return true, nil
}

// addUserToGroup makes username a member of group, and reports whether that
// was a change.
func addUserToGroup(username, group string) (bool, error) {
	member, err := isMember(username, group)
	if err != nil {
		return false, err
	}
	if member {
		return false, nil
	}

	if err := run("usermod", "-aG", group, username); err != nil {
		return false, err
	}

	return true, nil
}

// isMember reports whether username already belongs to group.
func isMember(username, group string) (bool, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return false, errors.Wrapf(err, "looking up user %q", username)
	}

	found, err := user.LookupGroup(group)
	if err != nil {
		// The group is created before this runs, so a failure here is not the
		// ordinary "not yet created" case and is worth reporting.
		return false, errors.Wrapf(err, "looking up group %q", group)
	}

	ids, err := account.GroupIds()
	if err != nil {
		return false, errors.Wrapf(err, "reading the groups of %q", username)
	}

	return slices.Contains(ids, found.Gid), nil
}

// startService brings the unit up, or leaves a live tunnel alone.
func startService(steps Steps, logf func(format string, args ...any)) error {
	if err := run("systemctl", "daemon-reload"); err != nil {
		return err
	}

	if err := run("systemctl", "enable", unitName); err != nil {
		return err
	}

	if !isActive() {
		if err := run("systemctl", "start", unitName); err != nil {
			return err
		}
		logf("started %s", unitName)

		return nil
	}

	// Replacing the binary of a running daemon does not affect the process
	// already started from it, so a restart is what actually completes the
	// upgrade — and a restart takes the tunnel down with it.
	if tunnelIsUp() && !steps.Restart {
		logf("svpnd is running with a live connection and was left alone; the new binary takes effect after \"svpn down\" and \"systemctl restart %s\", or re-run with --restart to do it now",
			unitName)

		return nil
	}

	if err := run("systemctl", "restart", unitName); err != nil {
		return err
	}
	logf("restarted %s", unitName)

	return nil
}

// isActive reports whether the unit is currently running.
func isActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", unitName).Run() == nil
}

// tunnelIsUp asks the running daemon whether it is carrying a connection.
//
// Anything that goes wrong answers "no": the question only exists to avoid
// dropping a tunnel that is in use, and a daemon that cannot be reached is not
// carrying one that this restart would interrupt.
func tunnelIsUp() bool {
	client, err := ipc.NewClient(ipc.DefaultSocket)
	if err != nil {
		return false
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(ipc.Request{Command: ipc.CommandStatus}, statusTimeout)
	if err != nil || response.Status == nil {
		return false
	}

	return response.Status.State != ipc.StateDisconnected
}

// run executes one of the system commands an installation needs.
//
// The argument vectors are built from resolved settings, never from anything a
// client sends, and no shell is involved.
func run(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput() //nolint:gosec
	if err != nil {
		// The command's own words carry the diagnosis; the exit status only
		// says that something failed, so it lands last as the wrapped cause.
		failure := errors.Wrapf(err, "%s %s: %s", name, strings.Join(args, " "), strings.TrimSpace(string(output)))

		// groupadd, usermod and systemctl all need root whatever --prefix
		// says, so an unprivileged run has one likely explanation and it is
		// worth naming rather than leaving the reader with "Permission denied".
		if os.Geteuid() != 0 {
			return errors.Wrapf(failure, "this needs root: re-run under sudo, or pass --group with a group you are already in and --no-service to exercise the rest")
		}

		return failure
	}

	return nil
}
