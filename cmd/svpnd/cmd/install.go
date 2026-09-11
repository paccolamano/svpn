package cmd

import (
	"fmt"
	"log/slog"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/internal/install"
)

var cmdInstall = &cobra.Command{
	Use:     "install",
	Short:   "Install svpnd as a system service",
	Version: cmd.Version,
	Long: `Install svpnd as a system service.

Copies svpn and svpnd under the prefix, creates the group that owns the control
socket, adds the invoking user to it, writes the systemd unit and starts the
service. Run it as root, from the directory holding both binaries — the one the
release archive was extracted into, or bin/ after a build.

The account added to the group is the one behind sudo or pkexec, never root:
the client is meant to run unprivileged, and the daemon's allowlist refuses uid
0 on purpose. Group membership does not reach sessions that are already open,
so the shell that runs this still needs a newgrp or a fresh login.

Running it again upgrades an existing installation. Flags already in the
environment file are kept, and a second user is added to the uid allowlist
rather than replacing the first.

--dry-run prints what would happen and changes nothing. Together with --prefix,
--unit-dir, --conf-dir and --no-service it is also how the installer is
exercised without touching the system.`,
	Args: cobra.NoArgs,
	Run:  run(runInstall),
}

type installOptions struct {
	prefix    string
	unitDir   string
	confDir   string
	group     string
	user      string
	noService bool
	restart   bool
	dryRun    bool
}

var installOpts installOptions

func init() {
	flags := cmdInstall.Flags()

	flags.StringVar(&installOpts.prefix, "prefix", install.DefaultPrefix, "install the binaries under this prefix, in bin/")
	flags.StringVar(&installOpts.unitDir, "unit-dir", install.DefaultUnitDir, "directory to write the systemd unit into")
	flags.StringVar(&installOpts.confDir, "conf-dir", install.DefaultConfDir, "directory to write the daemon's environment file into")
	flags.StringVar(&installOpts.group, "group", "", "group given access to the control socket; defaults to the Sorint one")
	flags.StringVar(&installOpts.user, "user", "", "account to add to the group; defaults to the user behind sudo or pkexec")
	flags.BoolVar(&installOpts.noService, "no-service", false, "write the files but leave systemd alone")
	flags.BoolVar(&installOpts.restart, "restart", false, "restart a running daemon even when it is carrying a connection, dropping it")
	flags.BoolVar(&installOpts.dryRun, "dry-run", false, "print what would be done and change nothing")

	cmdSVPND.AddCommand(cmdInstall)
}

func runInstall(c *cobra.Command, args []string) error {
	steps, err := install.Plan(install.Options{
		Prefix:    installOpts.prefix,
		UnitDir:   installOpts.unitDir,
		ConfDir:   installOpts.confDir,
		Group:     installOpts.group,
		User:      installOpts.user,
		NoService: installOpts.noService,
		Restart:   installOpts.restart,
	})
	if err != nil {
		return errors.Wrapf(err, "planning the installation")
	}

	if installOpts.dryRun {
		describe(steps.Describe())

		return nil
	}

	if err := install.Apply(steps, func(format string, args ...any) {
		slog.Info(fmt.Sprintf(format, args...))
	}); err != nil {
		return errors.Wrapf(err, "installing")
	}

	slog.Info("installed")

	return nil
}

// describe prints a plan for --dry-run.
//
// It goes to stdout rather than through slog: it is the command's output, not
// a log of what it did, and a caller may well want to read it with a pipe.
func describe(lines []string) {
	for _, line := range lines {
		fmt.Println(line)
	}
}
