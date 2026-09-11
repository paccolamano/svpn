package cmd

import (
	"fmt"
	"log/slog"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/internal/install"
)

var cmdUninstall = &cobra.Command{
	Use:     "uninstall",
	Short:   "Remove svpnd from the system",
	Version: cmd.Version,
	Long: `Remove svpnd from the system.

Stops and disables the service, then removes the unit and both binaries. The
service is stopped with a TERM, which is what lets the daemon close any live
tunnel and put the routing table back before it exits.

The group and the environment file are left in place: other users may still be
in the group, and the file holds settings that were edited by hand. --purge
removes both.

Without a package manager this is the only clean way back out, so it removes
exactly what install put in and nothing else.`,
	Args: cobra.NoArgs,
	Run:  run(runUninstall),
}

type uninstallOptions struct {
	prefix    string
	unitDir   string
	confDir   string
	group     string
	noService bool
	purge     bool
	dryRun    bool
}

var uninstallOpts uninstallOptions

func init() {
	flags := cmdUninstall.Flags()

	flags.StringVar(&uninstallOpts.prefix, "prefix", install.DefaultPrefix, "prefix the binaries were installed under")
	flags.StringVar(&uninstallOpts.unitDir, "unit-dir", install.DefaultUnitDir, "directory holding the systemd unit")
	flags.StringVar(&uninstallOpts.confDir, "conf-dir", install.DefaultConfDir, "directory holding the daemon's environment file")
	flags.StringVar(&uninstallOpts.group, "group", "", "group to remove with --purge; defaults to the Sorint one")
	flags.BoolVar(&uninstallOpts.noService, "no-service", false, "remove the files but leave systemd alone")
	flags.BoolVar(&uninstallOpts.purge, "purge", false, "also remove the environment file and the socket group")
	flags.BoolVar(&uninstallOpts.dryRun, "dry-run", false, "print what would be done and change nothing")

	cmdSVPND.AddCommand(cmdUninstall)
}

func runUninstall(c *cobra.Command, args []string) error {
	steps := install.PlanUninstall(install.Options{
		Prefix:    uninstallOpts.prefix,
		UnitDir:   uninstallOpts.unitDir,
		ConfDir:   uninstallOpts.confDir,
		Group:     uninstallOpts.group,
		NoService: uninstallOpts.noService,
		Purge:     uninstallOpts.purge,
	})

	if uninstallOpts.dryRun {
		describe(steps.Describe())

		return nil
	}

	if err := install.ApplyUninstall(steps, func(format string, args ...any) {
		slog.Info(fmt.Sprintf(format, args...))
	}); err != nil {
		return errors.Wrapf(err, "uninstalling")
	}

	slog.Info("uninstalled")

	return nil
}
