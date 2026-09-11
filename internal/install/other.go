//go:build !linux

package install

import "github.com/sorintlab/errors"

// Apply refuses to run.
//
// Planning is portable — it is path arithmetic and a template — but carrying it
// out is not: this writes a systemd unit, and it hands the socket to a POSIX
// group. macOS wants a launchd plist and its own idea of who may connect;
// Windows wants a service and a named pipe with a security descriptor. Both are
// real work, and a partial installation is worse than an honest refusal.
func Apply(Steps, func(format string, args ...any)) error {
	return errors.New("installing the daemon as a service is not implemented on this platform yet")
}

// ApplyUninstall refuses to run, for the same reason as Apply.
func ApplyUninstall(UninstallSteps, func(format string, args ...any)) error {
	return errors.New("uninstalling the daemon is not implemented on this platform yet")
}
