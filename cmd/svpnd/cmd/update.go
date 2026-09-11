package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/cmd"
	"github.com/paccolamano/svpn/internal/install"
	"github.com/paccolamano/svpn/internal/release"
)

// downloadTimeout bounds the whole exchange with GitHub: resolving the release,
// fetching the checksums and pulling down the archive.
const downloadTimeout = 10 * time.Minute

var cmdUpdate = &cobra.Command{
	Use:     "update",
	Short:   "Update svpn and svpnd to the latest release",
	Version: cmd.Version,
	Long: `Update svpn and svpnd to the latest release.

Resolves the most recent release on GitHub, downloads the archive for this
platform, checks it against the published SHA-256 sums and then runs the
install command of the version just downloaded — so an upgrade that needs a
different installation procedure brings that procedure with it.

There is no package manager in this picture, so this is the update channel.
Run it as root: it replaces binaries under the prefix and restarts the service.

--check reports what is available and changes nothing. A development build is
refused unless --force is given, so an update never silently discards a binary
someone is working on.`,
	Args: cobra.NoArgs,
	Run:  run(runUpdate),
}

type updateOptions struct {
	check      bool
	force      bool
	repository string
	restart    bool
}

var updateOpts updateOptions

func init() {
	flags := cmdUpdate.Flags()

	flags.BoolVar(&updateOpts.check, "check", false, "report whether a newer release exists and change nothing")
	flags.BoolVar(&updateOpts.force, "force", false, "update even from a development build, or to a version that is not newer")
	flags.StringVar(&updateOpts.repository, "repository", release.DefaultRepository, "GitHub repository to take releases from")
	flags.BoolVar(&updateOpts.restart, "restart", false, "restart the daemon even when it is carrying a connection, dropping it")

	cmdSVPND.AddCommand(cmdUpdate)
}

func runUpdate(c *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(c.Context(), downloadTimeout)
	defer cancel()

	client := &release.Client{Repository: updateOpts.repository}

	latest, err := client.Latest(ctx)
	if err != nil {
		return errors.Wrapf(err, "resolving the latest release")
	}

	slog.Info("latest release", "version", latest.Tag, "installed", cmd.Version)

	upToDate := !release.Newer(latest.Tag, cmd.Version)
	if updateOpts.check {
		if upToDate {
			slog.Info("already up to date")
		} else {
			slog.Info("an update is available", "run", "sudo svpnd update")
		}

		return nil
	}

	if upToDate && !updateOpts.force {
		slog.Info("already up to date")

		return nil
	}

	// A build from the Makefile carries a commit hash, not a tag. Replacing it
	// with the last published release would throw away whatever was being
	// worked on, and the loss would be silent.
	if !release.IsRelease(cmd.Version) && !updateOpts.force {
		return errors.Errorf("this is a development build (%s), not a release; re-run with --force to replace it with %s", cmd.Version, latest.Tag)
	}

	archive, err := latest.Archive(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return errors.Wrapf(err, "choosing the archive to download")
	}
	checksums, err := latest.Checksums()
	if err != nil {
		return errors.Wrapf(err, "choosing the archive to download")
	}

	dir, err := os.MkdirTemp("", "svpn-update-*")
	if err != nil {
		return errors.Wrapf(err, "creating a temporary directory")
	}
	defer func() { _ = os.RemoveAll(dir) }()

	slog.Info("downloading", "asset", archive.Name)
	path, err := client.Download(ctx, archive, checksums, dir)
	if err != nil {
		return errors.Wrapf(err, "downloading %s", archive.Name)
	}

	slog.Info("checksum verified", "asset", archive.Name)

	if _, err := release.Extract(path, dir); err != nil {
		return errors.Wrapf(err, "unpacking %s", archive.Name)
	}

	return handOver(ctx, dir)
}

// handOver runs the install command of the version just downloaded.
//
// The new binary installs itself rather than being copied into place here: an
// upgrade that changes where files go, or adds a step, then carries that
// knowledge with it instead of depending on whatever version happened to be
// installed when the update was started.
func handOver(ctx context.Context, dir string) error {
	installer := filepath.Join(dir, "svpnd")
	if _, err := os.Stat(installer); err != nil {
		return errors.Wrapf(err, "the archive does not contain svpnd")
	}

	prefix, err := installedPrefix()
	if err != nil {
		return err
	}

	args := []string{"install", "--prefix", prefix}
	if updateOpts.restart {
		args = append(args, "--restart")
	}

	slog.Info("installing", "prefix", prefix)

	// The vector is a path inside a temporary directory this process created,
	// plus this command's own flags, and the archive it came out of was checked
	// against its published sum above. No shell is involved.
	command := exec.CommandContext(ctx, installer, args...) //nolint:gosec
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	if err := command.Run(); err != nil {
		return errors.Wrapf(err, "running the installer from %s", installer)
	}

	return nil
}

// installedPrefix works out where this binary was installed, so an update
// stays where the previous installation put it rather than defaulting back to
// /usr/local and leaving two copies behind.
func installedPrefix() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", errors.Wrapf(err, "locating the running executable")
	}

	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", errors.Wrapf(err, "resolving %s", executable)
	}

	binDir := filepath.Dir(resolved)
	if filepath.Base(binDir) != "bin" {
		// Updating something that was never installed — a binary run straight
		// out of a build directory — would put it somewhere it never was.
		return "", errors.Errorf("%s is not in a bin directory, so this does not look like an installed svpnd; install it first with \"%s\"",
			resolved, fmt.Sprintf("sudo %s install", resolved))
	}

	prefix := filepath.Dir(binDir)
	if prefix == "" {
		prefix = install.DefaultPrefix
	}

	return prefix, nil
}
