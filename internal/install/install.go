// Package install sets the daemon up as a system service, and takes it back
// out again.
//
// It exists so that the group, the unit file and the uid allowlist are decided
// by the binary that already knows the socket path and the default group,
// rather than by a list of commands in a README that drifts from the code. It
// is the single privileged entry point: the shell installer, `make install`
// and — eventually — a desktop GUI under pkexec all reach the system through
// here.
//
// Plan decides everything and touches nothing; Apply carries it out. The split
// is what makes an installation inspectable with --dry-run, and testable
// without root.
package install

import (
	_ "embed"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/desktop"
	"github.com/paccolamano/svpn/internal/sorint"
)

// Where an installation puts things by default.
//
// The prefix is /usr/local, not /usr: this is software installed outside the
// package manager, which is what /usr/local is for, and it leaves /usr/bin
// free for a distribution package that may exist one day.
const (
	DefaultPrefix  = "/usr/local"
	DefaultUnitDir = "/etc/systemd/system"
	DefaultConfDir = "/etc/svpn"

	// UnitName and ConfName are the two files an installation writes outside
	// the prefix.
	UnitName = "svpnd.service"
	ConfName = "svpnd.env"

	// argsVar is the environment variable the unit expands into svpnd's
	// argument list. Keeping the flags here rather than in ExecStart means an
	// upgrade can replace the unit without discarding local settings.
	argsVar = "SVPND_ARGS"
)

// binaries are installed under <prefix>/bin, always under these names
// regardless of what the running executable happens to be called.
//
// The desktop client is separate because it is conditional twice over: it
// needs cgo and a GL toolchain, so it is not built for every architecture the
// other two are released for, and a headless machine has no use for it. An
// install takes it when the payload has one, which makes "the release for this
// architecture has no GUI" and "--no-gui" the same code path and neither a
// special case.
var (
	binaries  = []string{"svpn", "svpnd"}
	guiBinary = "svpn-gui"
)

//go:embed svpnd.service.tmpl
var unitTemplate string

// executablePath is os.Executable, as a variable so tests can point the
// installer at a directory of their own.
var executablePath = os.Executable

// Options configures an installation.
//
// The path fields exist as real options, not constants, because they are what
// makes the whole thing exercisable: pointed at a temporary directory, an
// install runs to completion unprivileged and can be inspected afterwards.
type Options struct {
	// Prefix holds bin/svpn and bin/svpnd. Empty selects DefaultPrefix.
	Prefix string
	// UnitDir receives the systemd unit. Empty selects DefaultUnitDir.
	UnitDir string
	// ConfDir receives the environment file holding svpnd's flags. Empty
	// selects DefaultConfDir.
	ConfDir string
	// Group is handed the daemon's control socket. Empty selects the Sorint
	// default, which is what the client expects to find.
	Group string
	// User is added to Group. Empty resolves it from the environment sudo or
	// pkexec left behind.
	User string
	// NoService writes the files but leaves systemd alone. It is what makes an
	// installation into a temporary directory possible.
	NoService bool
	// Restart replaces a running daemon even when it is carrying a tunnel,
	// which drops the connection.
	Restart bool
	// Purge, on an uninstall, also removes the group and the environment file.
	Purge bool
	// NoGUI skips the desktop client even when the payload carries one, for a
	// machine that will never have a display.
	NoGUI bool
}

// File is one file an installation writes.
type File struct {
	Path    string
	Mode    os.FileMode
	Content string
}

// BinaryCopy is one binary to place under the prefix.
type BinaryCopy struct {
	From string
	To   string
	// Same reports that the source and the destination are already the same
	// file, which happens when an installed svpnd installs itself again.
	Same bool
}

// Steps is everything an installation will do, decided before anything has
// been touched.
type Steps struct {
	Binaries []BinaryCopy
	// Group is handed the control socket.
	Group string
	// User is added to Group; empty when no account could be resolved, which
	// is not fatal — the installation just cannot know whose it is.
	User string
	// UID is the account's numeric id, and -1 when User is empty. It is what
	// goes into the daemon's --allow-uid allowlist.
	UID int
	// Unit and Conf are the systemd unit and the environment file holding the
	// daemon's flags.
	Unit File
	Conf File
	// Desktop and Icon are the launcher entry and its artwork, and are written
	// only when the desktop client is part of this installation. Both are the
	// zero File otherwise, which is what GUI reports.
	//
	// Icon.Content holds PNG bytes rather than text. Every other File here is
	// a rendered template, but a second type for one field would be worse than
	// the surprise: a Go string is a byte sequence and writeFile does not care.
	Desktop File
	Icon    File
	// GUI reports that the desktop client is being installed.
	GUI bool
	// Service reports whether systemd should be told about any of this.
	Service bool
	// Restart carries Options.Restart through to Apply.
	Restart bool
}

// Plan decides what an installation would do. It reads, but changes nothing.
func Plan(options Options) (Steps, error) {
	paths := resolvePaths(options)

	sourceDir, err := sourceDirectory()
	if err != nil {
		return Steps{}, err
	}

	copies := make([]BinaryCopy, 0, len(binaries)+1)
	for _, name := range binaries {
		from := filepath.Join(sourceDir, name)
		to := filepath.Join(paths.binDir, name)

		if _, err := os.Stat(from); err != nil {
			// Running svpnd alone out of a directory is the normal way to get
			// here, and "no such file" does not suggest what is missing.
			return Steps{}, errors.Wrapf(err,
				"%s is not next to svpnd in %s; run this from the directory the release archive was extracted into, or from bin/ after a build",
				name, sourceDir)
		}

		copies = append(copies, BinaryCopy{From: from, To: to, Same: sameFile(from, to)})
	}

	// The desktop client, if the payload has one and the caller wants it. Its
	// absence is not an error: see the comment on guiBinary.
	withGUI := false
	if !options.NoGUI {
		from := filepath.Join(sourceDir, guiBinary)
		if _, err := os.Stat(from); err == nil {
			to := filepath.Join(paths.binDir, guiBinary)
			copies = append(copies, BinaryCopy{From: from, To: to, Same: sameFile(from, to)})
			withGUI = true
		}
	}

	target := resolveTargetUser(options.User, os.Getenv)
	account, err := lookupTargetUser(target)
	if err != nil {
		return Steps{}, err
	}

	steps := Steps{
		Binaries: copies,
		Group:    paths.group,
		UID:      -1,
		Service:  !options.NoService,
		Restart:  options.Restart,
		GUI:      withGUI,
	}

	if withGUI {
		entry := desktop.Entry{
			Name:    sorint.AppName,
			Comment: sorint.AppComment,
			Exec:    filepath.Join(paths.binDir, guiBinary),
			// The icon's name in the theme, not a path: the file below puts it
			// where a lookup will find it.
			Icon: sorint.AppID,
		}
		steps.Desktop = File{
			Path:    desktop.LauncherPath(paths.prefix, sorint.AppID),
			Mode:    0o644,
			Content: entry.Render(),
		}
		steps.Icon = File{
			Path:    desktop.IconPath(paths.prefix, sorint.AppID),
			Mode:    0o644,
			Content: string(sorint.Icon),
		}
	}

	if account != nil {
		uid, err := strconv.Atoi(account.Uid)
		if err != nil {
			return Steps{}, errors.Wrapf(err, "user %q has a non-numeric id %q", account.Username, account.Uid)
		}
		steps.User = account.Username
		steps.UID = uid
	}

	unit, err := renderUnit(filepath.Join(paths.binDir, "svpnd"), paths.confFile, paths.group)
	if err != nil {
		return Steps{}, err
	}
	steps.Unit = File{Path: paths.unitFile, Mode: 0o644, Content: unit}

	// Read rather than replace: a second user installing must be added to the
	// allowlist, not substituted for the first, and any flag added by hand has
	// to survive an upgrade.
	existing, err := os.ReadFile(paths.confFile)
	if err != nil && !os.IsNotExist(err) {
		return Steps{}, errors.Wrapf(err, "reading %s", paths.confFile)
	}
	steps.Conf = File{
		Path:    paths.confFile,
		Mode:    0o644,
		Content: mergeConf(string(existing), steps.UID),
	}

	return steps, nil
}

// Describe renders the plan as the lines --dry-run prints.
func (s Steps) Describe() []string {
	lines := make([]string, 0, len(s.Binaries)+6)

	lines = append(lines, "ensure group "+s.Group+" exists")
	if s.User != "" {
		// "ensure", not "add": whether it is already a member is a question for
		// Apply, and a plan that promised an addition would often be wrong.
		lines = append(lines, "ensure "+s.User+" (uid "+strconv.Itoa(s.UID)+") is in group "+s.Group)
	} else {
		lines = append(lines, "no account to add to group "+s.Group+": neither SUDO_USER nor PKEXEC_UID names one")
	}

	for _, binary := range s.Binaries {
		if binary.Same {
			lines = append(lines, "leave "+binary.To+" alone (already the installed binary)")

			continue
		}
		lines = append(lines, "install "+binary.From+" -> "+binary.To)
	}

	lines = append(lines, "write "+s.Unit.Path, "write "+s.Conf.Path)

	if s.GUI {
		lines = append(lines, "write "+s.Desktop.Path, "write "+s.Icon.Path)
	} else {
		// True of both causes: a release for an architecture the client is
		// not built for, and --no-gui.
		lines = append(lines, "no desktop client in this installation, so no launcher entry")
	}

	if s.Service {
		lines = append(lines, "systemctl daemon-reload, enable and start "+UnitName)
	} else {
		lines = append(lines, "leave systemd alone (--no-service)")
	}

	return lines
}

// UninstallSteps is what an uninstall will remove.
type UninstallSteps struct {
	// Paths are removed in order: the unit before the binaries, so that a
	// failure part way through never leaves systemd pointing at a binary that
	// is no longer there.
	Paths []string
	// Group is removed only with Purge, because other users may still be in it.
	Group   string
	Service bool
	Purge   bool
}

// PlanUninstall decides what an uninstall would remove.
func PlanUninstall(options Options) UninstallSteps {
	paths := resolvePaths(options)

	steps := UninstallSteps{
		Paths:   []string{paths.unitFile},
		Service: !options.NoService,
		Purge:   options.Purge,
	}

	if options.Purge {
		steps.Paths = append(steps.Paths, paths.confFile)
		steps.Group = paths.group
	}

	for _, name := range binaries {
		steps.Paths = append(steps.Paths, filepath.Join(paths.binDir, name))
	}

	// Listed unconditionally: ApplyUninstall skips a path that is not there,
	// so an installation that never had a desktop client needs no check here
	// and one that did is cleaned up whatever this binary was built with.
	steps.Paths = append(steps.Paths,
		filepath.Join(paths.binDir, guiBinary),
		desktop.LauncherPath(paths.prefix, sorint.AppID),
		desktop.IconPath(paths.prefix, sorint.AppID),
	)

	return steps
}

// HasGUI reports whether an installation under prefix includes the desktop
// client.
//
// svpnd update asks before handing over: the archive carries a desktop client
// whether or not this machine wanted one, and a server that installed with
// --no-gui would otherwise acquire a launcher entry the first time it updated.
func HasGUI(prefix string) bool {
	if prefix == "" {
		prefix = DefaultPrefix
	}

	_, err := os.Stat(filepath.Join(prefix, "bin", guiBinary))

	return err == nil
}

// sameFile reports that source and destination are already the same file,
// which happens when an installed svpnd installs itself again.
func sameFile(from, to string) bool {
	source, err := os.Stat(from)
	if err != nil {
		return false
	}

	destination, err := os.Stat(to)
	if err != nil {
		return false
	}

	return os.SameFile(source, destination)
}

// Describe renders the plan as the lines --dry-run prints.
func (s UninstallSteps) Describe() []string {
	lines := make([]string, 0, len(s.Paths)+2)

	if s.Service {
		lines = append(lines, "systemctl disable --now "+UnitName)
	}
	for _, path := range s.Paths {
		lines = append(lines, "remove "+path)
	}
	if s.Purge && s.Group != "" {
		lines = append(lines, "remove group "+s.Group)
	}

	return lines
}

// paths are the resolved destinations of an installation.
type paths struct {
	prefix   string
	binDir   string
	unitFile string
	confFile string
	group    string
}

// resolvePaths applies the defaults. It is separate so that every other
// function works from resolved values and never has to repeat the fallbacks.
func resolvePaths(options Options) paths {
	prefix := options.Prefix
	if prefix == "" {
		prefix = DefaultPrefix
	}

	unitDir := options.UnitDir
	if unitDir == "" {
		unitDir = DefaultUnitDir
	}

	confDir := options.ConfDir
	if confDir == "" {
		confDir = DefaultConfDir
	}

	group := options.Group
	if group == "" {
		group = sorint.SocketGroup
	}

	return paths{
		prefix:   prefix,
		binDir:   filepath.Join(prefix, "bin"),
		unitFile: filepath.Join(unitDir, UnitName),
		confFile: filepath.Join(confDir, ConfName),
		group:    group,
	}
}

// sourceDirectory is where the binaries to install are, which is wherever the
// running one came from: the extracted archive, or bin/ after a build.
func sourceDirectory() (string, error) {
	executable, err := executablePath()
	if err != nil {
		return "", errors.Wrapf(err, "locating the running executable")
	}

	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", errors.Wrapf(err, "resolving %s", executable)
	}

	return filepath.Dir(resolved), nil
}

// targetUser is the account an installation adds to the socket group, named
// either by username or by numeric id depending on where it was found.
type targetUser struct {
	Name string
	UID  string
}

// resolveTargetUser decides whose account should be allowed to drive the VPN.
//
// The whole point of the daemon is that the client runs unprivileged, so the
// account that matters is never the one running this command. sudo and pkexec
// each leave the original identity behind in a different variable, and pkexec
// is how a desktop GUI will ask for the privilege to install.
func resolveTargetUser(explicit string, getenv func(string) string) targetUser {
	if explicit != "" {
		return targetUser{Name: explicit}
	}

	// SUDO_USER is root when root itself ran sudo. Adding root to the group
	// would be pointless: the daemon's allowlist refuses uid 0 by design.
	if name := getenv("SUDO_USER"); name != "" && name != "root" {
		return targetUser{Name: name}
	}

	if uid := getenv("PKEXEC_UID"); uid != "" && uid != "0" {
		return targetUser{UID: uid}
	}

	return targetUser{}
}

// lookupTargetUser resolves a decision from resolveTargetUser into an account,
// or nil when there was nothing to resolve.
func lookupTargetUser(target targetUser) (*user.User, error) {
	switch {
	case target.Name != "":
		account, err := user.Lookup(target.Name)
		if err != nil {
			return nil, errors.Wrapf(err, "looking up user %q", target.Name)
		}

		return account, nil

	case target.UID != "":
		account, err := user.LookupId(target.UID)
		if err != nil {
			return nil, errors.Wrapf(err, "looking up uid %s", target.UID)
		}

		return account, nil

	default:
		return nil, nil
	}
}

// unitParams are what the embedded template needs.
type unitParams struct {
	Binary   string
	ConfFile string
	Group    string
}

// renderUnit produces the systemd unit for a particular installation.
func renderUnit(binary, confFile, group string) (string, error) {
	parsed, err := template.New(UnitName).Parse(unitTemplate)
	if err != nil {
		return "", errors.Wrapf(err, "parsing the unit template")
	}

	var rendered strings.Builder
	if err := parsed.Execute(&rendered, unitParams{Binary: binary, ConfFile: confFile, Group: group}); err != nil {
		return "", errors.Wrapf(err, "rendering the unit")
	}

	return rendered.String(), nil
}

// confHeader explains the file to whoever opens it next.
const confHeader = `# Flags for svpnd, expanded into its command line by the systemd unit.
#
# Written by "svpnd install", which merges into this file rather than
# replacing it: an id added here by hand survives the next install, and a
# second user installing is added to the allowlist instead of replacing the
# first.
`

// mergeConf returns the environment file with uid added to the allowlist,
// preserving every other flag already in it. A uid below zero means no account
// could be resolved, and the allowlist is left as it was.
//
// The allowlist accumulates rather than being replaced: a second person
// installing has to be added to it, not substituted for the first, or
// installing for one user locks out everyone else.
//
// Without an allowlist the daemon accepts anyone who can open the socket and
// says so at startup. An installation knows exactly whose machine this is, so
// it is the one place that can close that gap without being asked.
func mergeConf(existing string, uid int) string {
	uids, others := parseArgs(existing)

	if uid >= 0 {
		uids[uid] = true
	}

	var args []string
	if len(uids) > 0 {
		ordered := make([]int, 0, len(uids))
		for value := range uids {
			ordered = append(ordered, value)
		}
		sort.Ints(ordered)

		formatted := make([]string, 0, len(ordered))
		for _, value := range ordered {
			formatted = append(formatted, strconv.Itoa(value))
		}

		args = append(args, "--allow-uid", strings.Join(formatted, ","))
	}
	args = append(args, others...)

	return confHeader + argsVar + "=" + strings.Join(args, " ") + "\n"
}

// parseArgs splits an existing environment file into the uids of its allowlist
// and every other flag, which are carried over untouched.
func parseArgs(existing string) (map[int]bool, []string) {
	uids := map[int]bool{}
	var others []string

	fields := strings.Fields(argsValue(existing))
	for i := 0; i < len(fields); i++ {
		field := fields[i]

		values, ok := flagValue(fields, &i, "--allow-uid")
		if !ok {
			others = append(others, field)

			continue
		}

		for _, value := range strings.Split(values, ",") {
			// A malformed id is dropped rather than carried forward: it could
			// never have matched a caller, and svpnd refuses to start on one.
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 0 {
				uids[parsed] = true
			}
		}
	}

	return uids, others
}

// flagValue reads one flag's value in either of the two forms systemd's
// argument splitting produces, "--name value" and "--name=value", advancing
// the index past the value when it was a separate field.
func flagValue(fields []string, i *int, name string) (string, bool) {
	field := fields[*i]

	if field == name && *i+1 < len(fields) {
		*i++

		return fields[*i], true
	}

	if after, found := strings.CutPrefix(field, name+"="); found {
		return after, true
	}

	return "", false
}

// argsValue extracts the value of the arguments variable from an environment
// file, taking the last assignment as systemd itself would.
func argsValue(existing string) string {
	value := ""
	for _, line := range strings.Split(existing, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, argsVar+"=") {
			continue
		}

		value = strings.Trim(strings.TrimPrefix(trimmed, argsVar+"="), `"'`)
	}

	return value
}
