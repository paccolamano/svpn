// Package desktop renders XDG desktop entries.
//
// There are two of them and they sit at opposite ends of the privilege scale:
// the launcher entry svpnd install writes under the prefix, for everyone on
// the machine, and the autostart entry the desktop client writes into the
// user's own ~/.config/autostart. Same format, same fields, and one place that
// knows how to spell it — which is only possible because the client and the
// installer are one module.
//
// Nothing here writes anything. Rendering is separate from applying for the
// same reason install.Plan is separate from install.Apply: it is what lets the
// result be checked without root and without a desktop session.
package desktop

import (
	"path/filepath"
	"strings"
)

// Entry is one desktop entry.
type Entry struct {
	// Name is what a launcher shows.
	Name string
	// Comment is the one-line description under it.
	Comment string
	// Exec is the absolute path to the binary.
	Exec string
	// Icon is the icon's name in the theme, or an absolute path to a file.
	Icon string
	// Categories place the entry in a menu. Empty means Network.
	Categories []string
	// Autostart marks the entry as one the session launches at login. It adds
	// the key GNOME and its derivatives read to let a user turn the entry off
	// without deleting it, which is the difference between a preference and a
	// file that keeps coming back.
	Autostart bool
}

// Render writes the entry in the format of the XDG Desktop Entry
// Specification.
func (e Entry) Render() string {
	categories := e.Categories
	if len(categories) == 0 {
		categories = []string{"Network"}
	}

	var b strings.Builder

	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Version=1.0\n")
	b.WriteString("Name=" + e.Name + "\n")
	if e.Comment != "" {
		b.WriteString("Comment=" + e.Comment + "\n")
	}
	b.WriteString("Exec=" + e.Exec + "\n")
	if e.Icon != "" {
		b.WriteString("Icon=" + e.Icon + "\n")
	}
	b.WriteString("Terminal=false\n")
	// The trailing semicolon is required: Categories is a list, and the
	// specification terminates every entry in one, including the last.
	b.WriteString("Categories=" + strings.Join(categories, ";") + ";\n")
	b.WriteString("StartupNotify=true\n")

	if e.Autostart {
		b.WriteString("X-GNOME-Autostart-enabled=true\n")
	}

	return b.String()
}

// LauncherPath is where a machine-wide entry named id goes under prefix.
func LauncherPath(prefix, id string) string {
	return filepath.Join(prefix, "share", "applications", id+".desktop")
}

// IconPath is where the launcher's artwork goes under prefix.
//
// share/pixmaps rather than a size directory under share/icons/hicolor: a
// hicolor directory declares the exact pixel size of what is in it, and this
// icon is 212x212, which is not one of the sizes the theme defines. pixmaps
// has no size convention and every desktop environment still reads it.
func IconPath(prefix, id string) string {
	return filepath.Join(prefix, "share", "pixmaps", id+".png")
}

// AutostartPath is where a user's own autostart entry goes in their home.
func AutostartPath(home, id string) string {
	return filepath.Join(home, ".config", "autostart", id+".desktop")
}
