package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/internal/release"
	"github.com/paccolamano/svpn/pkg/ipc"
)

// checkTimeout bounds the question to GitHub. It is short: this is a courtesy
// check, and a slow network should not hold up a command the user typed.
const checkTimeout = 15 * time.Second

// unknownDaemonVersion describes a daemon from before it reported one at all.
const unknownDaemonVersion = "older than this client"

var cmdVersion = &cobra.Command{
	Use:     "version",
	Short:   "Report the client and daemon versions",
	Version: cmd.Version,
	Long: `Report the client and daemon versions.

The two are separate binaries, and since each can be updated on its own they
can disagree. This prints both, asking the daemon for its own, so a mismatch is
something you can see rather than something you infer from odd behaviour.

--check also asks GitHub whether a newer release exists. It does not update
anything: the binaries live where only root can write them, so the update
itself belongs to the privileged half and is "sudo svpnd update".`,
	Args: cobra.NoArgs,
	Run:  run(version),
}

type versionOptions struct {
	daemon     daemonOptions
	check      bool
	repository string
}

var versionOpts versionOptions

func init() {
	flags := cmdVersion.Flags()

	versionOpts.daemon.register(flags)
	flags.BoolVar(&versionOpts.check, "check", false, "ask GitHub whether a newer release exists")
	flags.StringVar(&versionOpts.repository, "repository", release.DefaultRepository, "GitHub repository to check for releases")

	cmdSVPN.AddCommand(cmdVersion)
}

func version(c *cobra.Command, args []string) error {
	fmt.Printf("client:  %s\n", cmd.Version)
	fmt.Printf("daemon:  %s\n", daemonVersion(versionOpts.daemon))

	if !versionOpts.check {
		return nil
	}

	ctx, cancel := context.WithTimeout(c.Context(), checkTimeout)
	defer cancel()

	latest, err := (&release.Client{Repository: versionOpts.repository}).Latest(ctx)
	if err != nil {
		return errors.Wrapf(err, "checking for a newer release")
	}

	fmt.Printf("latest:  %s\n", latest.Tag)
	if release.Newer(latest.Tag, cmd.Version) {
		fmt.Println()
		fmt.Println("An update is available. Install it with:")
		fmt.Println("  sudo svpnd update")
	}

	return nil
}

// daemonVersion asks the daemon what it is running, and describes why it could
// not be asked when that fails.
//
// Not reaching the daemon is an ordinary answer here rather than an error: the
// service may simply not be running, and the client's own version is still
// worth printing.
func daemonVersion(options daemonOptions) string {
	client, err := ipc.NewClient(options.socket)
	if err != nil {
		return "not running"
	}
	defer func() { _ = client.Close() }()

	response, err := client.Do(ipc.Request{Command: ipc.CommandStatus}, checkTimeout)
	if err != nil || response.Status == nil {
		return "unreachable"
	}
	if response.Status.Version == "" {
		// A daemon from before this field existed. Saying so is more use than
		// an empty line, and it is itself a reason to update.
		return unknownDaemonVersion
	}

	return response.Status.Version
}
