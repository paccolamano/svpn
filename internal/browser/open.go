// Package browser opens URLs in the user's default browser.
package browser

import (
	"os/exec"
	"runtime"

	"github.com/sorintlab/errors"
)

// Open launches the default browser on url.
//
// The URL carries query parameters, so the command is built as an argument
// vector rather than a shell string: on Windows `start` would swallow
// everything after the first `&`.
func Open(url string) error {
	var cmd *exec.Cmd

	// The URL is a variable, but no shell ever sees it: each command below is
	// an argument vector, so nothing in the query string can be interpreted as
	// a command. That is the same reason the vectors are built this way.
	switch runtime.GOOS {
	case "windows":
		// rundll32 takes the URL as a single argument and needs no shell, so
		// `&` in the query string survives intact.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //nolint:gosec
	case "darwin":
		cmd = exec.Command("open", url) //nolint:gosec
	default:
		cmd = exec.Command("xdg-open", url) //nolint:gosec
	}

	if err := cmd.Start(); err != nil {
		return errors.Wrapf(err, "launching browser")
	}

	// The browser outlives this process; reap it so it does not linger as a
	// zombie for the lifetime of the daemon.
	go func() { _ = cmd.Wait() }()

	return nil
}
