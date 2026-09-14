package auth

import (
	"os/exec"
	"runtime"

	"github.com/sorintlab/errors"
)

// openBrowser launches the default browser on url.
//
// It used to be internal/browser, and before that pkg/browser, back when a GUI
// in another module might have wanted it. Nothing outside this package ever
// did: a client that has its own opener passes it as Options.OpenBrowser, and
// one that has not gets this.
//
// The URL carries query parameters, so the command is built as an argument
// vector rather than a shell string: on Windows `start` would swallow
// everything after the first `&`.
func openBrowser(url string) error {
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
