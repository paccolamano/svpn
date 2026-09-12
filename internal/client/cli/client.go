package cli

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/pflag"

	"github.com/paccolamano/svpn/internal/client"
	"github.com/paccolamano/svpn/internal/ipc"
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
	response, err := client.Client{Socket: options.socket}.Do(request, daemonTimeout)
	if err != nil {
		return err
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

// warnOnVersionMismatch says on stderr what internal/client worked out. The
// GUI shows the same mismatch in its own way, which is why the deciding is
// there and only the wording is here.
func warnOnVersionMismatch(status *ipc.Status) {
	mismatch, ok := client.CheckVersion(status)
	if !ok {
		return
	}

	if mismatch.Daemon == "" {
		slog.Warn(mismatch.Message(), "client", mismatch.Client, "hint", mismatch.Hint)

		return
	}

	slog.Warn(mismatch.Message(),
		"client", mismatch.Client, "daemon", mismatch.Daemon, "hint", mismatch.Hint)
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
