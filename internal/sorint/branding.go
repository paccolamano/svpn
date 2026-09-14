package sorint

import _ "embed"

// AppID names the application to the desktop. It keys the preferences file,
// attributes a notification, and names both the desktop entry and the icon
// file an installation writes. It has to stay stable across releases.
const AppID = "it.sorint.svpn"

// AppName and AppComment are what a launcher shows.
const (
	AppName    = "svpn"
	AppComment = "Connect to the Sorint.LAB VPN"
)

// The artwork, embedded rather than read from disk because these ship inside
// single binaries: a file loaded from a path would be missing exactly when
// someone runs one out of a release archive.
//
// It lives here, with the gateway and the group, because it is the other half
// of what makes this build Sorint's rather than a generic FortiGate client —
// and because there are now two consumers: the desktop client draws it, and
// svpnd install writes the icon out for the launcher.
//
// These are bytes, not Fyne resources. svpnd imports this package, and typing
// the artwork as fyne.Resource would put an OpenGL toolchain in the dependency
// graph of a daemon that has no window.
var (
	//go:embed icon.png
	Icon []byte

	// The full logo in its two versions. The lettering is solid white in one
	// and solid black in the other, so the choice is not cosmetic: the wrong
	// one is invisible against its background.
	//
	//go:embed logo-on-dark.svg
	LogoOnDark []byte
	//go:embed logo-on-light.svg
	LogoOnLight []byte
)
