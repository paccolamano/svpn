package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/paccolamano/svpn/internal/sorint"
)

func TestResolvePathsAppliesTheDefaults(t *testing.T) {
	resolved := resolvePaths(Options{})

	if resolved.binDir != "/usr/local/bin" {
		t.Errorf("binDir = %q, want /usr/local/bin", resolved.binDir)
	}
	if resolved.unitFile != "/etc/systemd/system/svpnd.service" {
		t.Errorf("unitFile = %q", resolved.unitFile)
	}
	if resolved.confFile != "/etc/svpn/svpnd.env" {
		t.Errorf("confFile = %q", resolved.confFile)
	}
	// The client looks for the socket by the same constant, so a default that
	// drifted from it would produce an installation no client could reach.
	if resolved.group != sorint.SocketGroup {
		t.Errorf("group = %q, want %q", resolved.group, sorint.SocketGroup)
	}
}

func TestResolvePathsHonoursOverrides(t *testing.T) {
	resolved := resolvePaths(Options{
		Prefix:  "/opt/svpn",
		UnitDir: "/tmp/units",
		ConfDir: "/tmp/conf",
		Group:   "vpnusers",
	})

	if resolved.binDir != "/opt/svpn/bin" {
		t.Errorf("binDir = %q", resolved.binDir)
	}
	if resolved.unitFile != "/tmp/units/svpnd.service" {
		t.Errorf("unitFile = %q", resolved.unitFile)
	}
	if resolved.confFile != "/tmp/conf/svpnd.env" {
		t.Errorf("confFile = %q", resolved.confFile)
	}
	if resolved.group != "vpnusers" {
		t.Errorf("group = %q", resolved.group)
	}
}

func TestResolveTargetUser(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		env      map[string]string
		want     targetUser
	}{
		{
			name:     "an explicit name wins over everything",
			explicit: "alice",
			env:      map[string]string{"SUDO_USER": "bob", "PKEXEC_UID": "1001"},
			want:     targetUser{Name: "alice"},
		},
		{
			name: "sudo names the account behind it",
			env:  map[string]string{"SUDO_USER": "bob"},
			want: targetUser{Name: "bob"},
		},
		{
			// This is the path a desktop GUI takes: pkexec sets no SUDO_USER.
			name: "pkexec leaves only a uid",
			env:  map[string]string{"PKEXEC_UID": "1001"},
			want: targetUser{UID: "1001"},
		},
		{
			name: "sudo is preferred when both are set",
			env:  map[string]string{"SUDO_USER": "bob", "PKEXEC_UID": "1001"},
			want: targetUser{Name: "bob"},
		},
		{
			// Adding root would be pointless: the daemon's allowlist refuses
			// uid 0, which is the whole reason the client runs unprivileged.
			name: "root behind sudo is not a target",
			env:  map[string]string{"SUDO_USER": "root"},
			want: targetUser{},
		},
		{
			name: "root behind pkexec is not a target",
			env:  map[string]string{"PKEXEC_UID": "0"},
			want: targetUser{},
		},
		{
			name: "a plain root login leaves nothing to resolve",
			env:  map[string]string{},
			want: targetUser{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveTargetUser(test.explicit, func(key string) string { return test.env[key] })
			if got != test.want {
				t.Errorf("resolveTargetUser() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestRenderUnitNamesTheInstalledBinary(t *testing.T) {
	unit, err := renderUnit("/opt/svpn/bin/svpnd", "/tmp/conf/svpnd.env", "vpnusers")
	if err != nil {
		t.Fatalf("renderUnit: %v", err)
	}

	// ExecStart must be the binary this installation actually placed. A unit
	// with a fixed path would start whatever else happened to be there.
	if !strings.Contains(unit, "ExecStart=/opt/svpn/bin/svpnd $SVPND_ARGS") {
		t.Errorf("unit does not start the installed binary:\n%s", unit)
	}
	if !strings.Contains(unit, "EnvironmentFile=-/tmp/conf/svpnd.env") {
		t.Errorf("unit does not read the environment file:\n%s", unit)
	}
	if !strings.Contains(unit, "Group=vpnusers") {
		t.Errorf("unit does not hand the socket to the group:\n%s", unit)
	}

	// The hardening is the reason a root daemon is tolerable at all, so a
	// template edit that drops it should fail here rather than in production.
	for _, directive := range []string{
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"KillSignal=SIGTERM",
	} {
		if !strings.Contains(unit, directive) {
			t.Errorf("unit lost %q", directive)
		}
	}

	if strings.Contains(unit, "{{") {
		t.Errorf("unit still holds an unrendered placeholder:\n%s", unit)
	}
}

func TestMergeConfAddsTheFirstUID(t *testing.T) {
	conf := mergeConf("", 1000)

	if !strings.Contains(conf, "SVPND_ARGS=--allow-uid 1000") {
		t.Errorf("conf = %q, want it to allow uid 1000", conf)
	}
}

func TestMergeConfKeepsExistingUIDs(t *testing.T) {
	// A second user installing must be added to the allowlist, not substituted
	// for the first, or installing for one person locks out everyone else.
	conf := mergeConf("SVPND_ARGS=--allow-uid 1000\n", 1001)

	if !strings.Contains(conf, "--allow-uid 1000,1001") {
		t.Errorf("conf = %q, want both uids", conf)
	}
}

func TestMergeConfKeepsFlagsAddedByHand(t *testing.T) {
	conf := mergeConf("SVPND_ARGS=--allow-uid 1000 --debug --host vpn.example.com\n", 1000)

	for _, want := range []string{"--allow-uid 1000", "--debug", "--host vpn.example.com"} {
		if !strings.Contains(conf, want) {
			t.Errorf("conf = %q, want it to keep %q", conf, want)
		}
	}
}

func TestMergeConfIsIdempotent(t *testing.T) {
	first := mergeConf("", 1000)
	second := mergeConf(first, 1000)

	if first != second {
		t.Errorf("re-running the installer changed the file:\n%q\n%q", first, second)
	}
}

func TestMergeConfAcceptsTheFormsSystemdDoes(t *testing.T) {
	tests := []struct {
		name     string
		existing string
	}{
		{name: "separate argument", existing: "SVPND_ARGS=--allow-uid 1000\n"},
		{name: "joined with an equals sign", existing: "SVPND_ARGS=--allow-uid=1000\n"},
		{name: "comma separated", existing: "SVPND_ARGS=--allow-uid=1000,1002\n"},
		{name: "quoted value", existing: "SVPND_ARGS=\"--allow-uid 1000\"\n"},
		{name: "with comments around it", existing: "# a note\nSVPND_ARGS=--allow-uid 1000\n# another\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conf := mergeConf(test.existing, 1001)

			if !strings.Contains(conf, "1000") || !strings.Contains(conf, "1001") {
				t.Errorf("conf = %q, want it to keep 1000 and add 1001", conf)
			}
		})
	}
}

func TestMergeConfWithoutAnAccountLeavesTheAllowlistAlone(t *testing.T) {
	// -1 is what Plan produces when neither sudo nor pkexec named anyone. The
	// daemon then warns at startup, which is the right outcome: better an
	// explicit warning than an allowlist naming the wrong person.
	conf := mergeConf("", -1)

	if strings.Contains(conf, "--allow-uid") {
		t.Errorf("conf = %q, want no allowlist", conf)
	}

	kept := mergeConf("SVPND_ARGS=--allow-uid 1000\n", -1)
	if !strings.Contains(kept, "--allow-uid 1000") {
		t.Errorf("conf = %q, want the existing allowlist kept", kept)
	}
}

func TestMergeConfDropsAnUnparseableUID(t *testing.T) {
	// svpnd refuses to start on one of these, so carrying it forward would
	// turn a typo into a daemon that never comes back up.
	conf := mergeConf("SVPND_ARGS=--allow-uid notanumber\n", 1000)

	if strings.Contains(conf, "notanumber") {
		t.Errorf("conf = %q, want the malformed id dropped", conf)
	}
	if !strings.Contains(conf, "--allow-uid 1000") {
		t.Errorf("conf = %q, want uid 1000", conf)
	}
}

// fakeInstallDir stands in for the directory the release archive was extracted
// into, and points Plan at it.
func fakeInstallDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range binaries {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/true\n"), 0o755); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	previous := executablePath
	executablePath = func() (string, error) { return filepath.Join(dir, "svpnd"), nil }
	t.Cleanup(func() { executablePath = previous })

	return dir
}

func TestPlanTakesBothBinariesFromTheSourceDirectory(t *testing.T) {
	source := fakeInstallDir(t)
	prefix := t.TempDir()

	steps, err := Plan(Options{Prefix: prefix, UnitDir: t.TempDir(), ConfDir: t.TempDir(), User: currentUser(t)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if len(steps.Binaries) != 2 {
		t.Fatalf("planned %d binaries, want 2", len(steps.Binaries))
	}
	for i, name := range binaries {
		if want := filepath.Join(source, name); steps.Binaries[i].From != want {
			t.Errorf("binary %d from %q, want %q", i, steps.Binaries[i].From, want)
		}
		if want := filepath.Join(prefix, "bin", name); steps.Binaries[i].To != want {
			t.Errorf("binary %d to %q, want %q", i, steps.Binaries[i].To, want)
		}
	}
}

func TestPlanRefusesWhenTheClientIsMissing(t *testing.T) {
	source := fakeInstallDir(t)
	if err := os.Remove(filepath.Join(source, "svpn")); err != nil {
		t.Fatalf("removing svpn: %v", err)
	}

	_, err := Plan(Options{Prefix: t.TempDir(), UnitDir: t.TempDir(), ConfDir: t.TempDir()})
	if err == nil {
		t.Fatal("Plan succeeded with only one binary present")
	}
	// Running svpnd on its own is the normal way to get here, and the error
	// has to say where the other half is expected to be.
	if !strings.Contains(err.Error(), "svpn is not next to svpnd") {
		t.Errorf("error = %v, want it to name the missing binary", err)
	}
}

func TestPlanMergesIntoAnExistingConf(t *testing.T) {
	fakeInstallDir(t)
	confDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(confDir, ConfName), []byte("SVPND_ARGS=--allow-uid 4242\n"), 0o644); err != nil {
		t.Fatalf("writing the existing conf: %v", err)
	}

	steps, err := Plan(Options{Prefix: t.TempDir(), UnitDir: t.TempDir(), ConfDir: confDir, User: currentUser(t)})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if !strings.Contains(steps.Conf.Content, "4242") {
		t.Errorf("conf = %q, want the existing uid kept", steps.Conf.Content)
	}
}

func TestPlanChangesNothing(t *testing.T) {
	fakeInstallDir(t)
	prefix := t.TempDir()
	unitDir := t.TempDir()
	confDir := t.TempDir()

	if _, err := Plan(Options{Prefix: prefix, UnitDir: unitDir, ConfDir: confDir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// --dry-run is only trustworthy if planning is genuinely read-only.
	for _, dir := range []string{prefix, unitDir, confDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("Plan wrote %d entries into %s", len(entries), dir)
		}
	}
}

func TestPlanUninstallRemovesTheUnitBeforeTheBinaries(t *testing.T) {
	steps := PlanUninstall(Options{Prefix: "/opt/svpn", UnitDir: "/tmp/units", ConfDir: "/tmp/conf"})

	if len(steps.Paths) == 0 || steps.Paths[0] != "/tmp/units/svpnd.service" {
		t.Fatalf("paths = %v, want the unit first", steps.Paths)
	}
	// Without --purge the group stays: other users may still be members, and
	// the environment file may hold settings that were edited by hand.
	if steps.Group != "" {
		t.Errorf("group = %q, want it left alone without --purge", steps.Group)
	}
	for _, path := range steps.Paths {
		if strings.HasSuffix(path, ConfName) {
			t.Errorf("paths = %v, want the environment file kept without --purge", steps.Paths)
		}
	}
}

func TestPlanUninstallPurgeRemovesTheGroupAndConf(t *testing.T) {
	steps := PlanUninstall(Options{Prefix: "/opt/svpn", UnitDir: "/tmp/units", ConfDir: "/tmp/conf", Purge: true})

	if steps.Group != sorint.SocketGroup {
		t.Errorf("group = %q, want %q", steps.Group, sorint.SocketGroup)
	}

	found := false
	for _, path := range steps.Paths {
		if path == "/tmp/conf/"+ConfName {
			found = true
		}
	}
	if !found {
		t.Errorf("paths = %v, want the environment file removed with --purge", steps.Paths)
	}
}

// currentUser names an account that is certain to exist, so that Plan's lookup
// resolves without depending on what the environment happens to hold.
func currentUser(t *testing.T) string {
	t.Helper()

	name := os.Getenv("USER")
	if name == "" {
		t.Skip("no USER in the environment to resolve")
	}

	return name
}
