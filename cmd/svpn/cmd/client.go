package cmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/pflag"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/pkg/ipc"
)

// daemonTimeout bounds a command's wait for the daemon. Connecting is the slow
// case: it allocates the session, negotiates PPP and rewrites the routing
// table before it answers.
const daemonTimeout = 2 * time.Minute

// daemonOptions are the flags every command that talks to the daemon carries.
type daemonOptions struct {
	socket string
	asJSON bool
}

func (o *daemonOptions) register(flags *pflag.FlagSet) {
	flags.StringVar(&o.socket, "socket", ipc.DefaultSocket, "daemon control socket")
	flags.BoolVar(&o.asJSON, "json", false, "print the raw response instead of a summary")
}

// ask sends one request to the daemon and reports what came back.
func ask(options daemonOptions, request ipc.Request) error {
	client, err := ipc.NewClient(options.socket)
	if err != nil {
		return unreachable(err, options.socket)
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(request, daemonTimeout)
	if err != nil {
		return errors.Wrapf(err, "asking the daemon to %s", request.Command)
	}

	warnOnVersionMismatch(response.Status)

	if options.asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(response); err != nil {
			return errors.Wrapf(err, "encoding the response")
		}
	} else {
		printStatus(response.Status)
	}

	if !response.OK {
		return errors.New(response.Error)
	}

	return nil
}

// unreachable explains why the daemon could not be reached.
//
// The dial error on its own is never the diagnosis: "no such file or
// directory" and "permission denied" are both true and neither says what to do.
// The two causes want opposite fixes, and telling them apart is the difference
// between a working installation and one that looks broken on first use.
func unreachable(err error, socket string) error {
	if !errors.Is(err, os.ErrPermission) {
		return errors.Wrapf(err, "svpnd is not reachable; check the service is running (systemctl status svpnd)")
	}

	if group, pending := ipc.PendingGroup(socket); pending {
		return errors.Wrapf(err,
			"you are in the %s group but this session started before that, so it does not have it yet; run \"newgrp %s\" or log out and back in",
			group, group)
	}

	return errors.Wrapf(err,
		"this account may not open %s; add it to the group that owns the socket with \"sudo svpnd install\", and do not use sudo to work around this — the daemon refuses uid 0 on purpose",
		socket)
}

// warnOnVersionMismatch reports a daemon built from something other than this
// client.
//
// The two halves are separate binaries with a protocol between them, and since
// they can be updated separately — svpnd update replaces both, but a GUI
// shipping its own client does not — a mismatch is now something that happens
// rather than something that cannot. It is a warning, not a refusal: the
// protocol is newline-delimited JSON and tolerates a field the other side does
// not know, so the versions differing is usually survivable and always worth
// knowing about when something behaves oddly.
func warnOnVersionMismatch(status *ipc.Status) {
	if status == nil || cmd.Version == "" {
		return
	}

	// An empty version is not "unknown": every build that reports one at all
	// sends something, even the "unknown" a plain `go build` leaves behind. So
	// a daemon saying nothing is one from before the field existed, which is a
	// mismatch that this client can be certain about.
	if status.Version == "" {
		slog.Warn("the daemon predates version reporting, so it is older than this client",
			"client", cmd.Version,
			"hint", "restart it to pick up the installed binary: sudo systemctl restart svpnd")

		return
	}

	if status.Version != cmd.Version {
		slog.Warn("the client and the daemon are different builds",
			"client", cmd.Version, "daemon", status.Version,
			"hint", "run \"sudo svpnd update\", or restart svpnd if it is still running an older binary")
	}
}

func printStatus(status *ipc.Status) {
	if status == nil {
		return
	}

	fmt.Printf("state:      %s\n", status.State)
	if status.Version != "" {
		fmt.Printf("daemon:     %s\n", status.Version)
	}
	if status.Gateway != "" {
		fmt.Printf("gateway:    %s\n", status.Gateway)
	}
	if status.Interface != "" {
		fmt.Printf("interface:  %s\n", status.Interface)
	}
	if status.LocalIP != "" {
		fmt.Printf("address:    %s\n", status.LocalIP)
	}
	if len(status.DNS) > 0 {
		fmt.Printf("DNS:        %s\n", strings.Join(status.DNS, ", "))
	}
	if len(status.Routes) > 0 {
		fmt.Printf("routes:     %s\n", strings.Join(status.Routes, ", "))
	}
	if status.BytesIn != 0 || status.BytesOut != 0 {
		fmt.Printf("traffic:    %d in / %d out\n", status.BytesIn, status.BytesOut)
	}
	if status.LastError != "" {
		fmt.Printf("last error: %s\n", status.LastError)
	}
}
