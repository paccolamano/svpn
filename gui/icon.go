package main

import (
	_ "embed"

	"fyne.io/fyne/v2"

	"github.com/paccolamano/svpn/gui/internal/ui"
)

// The artwork is embedded rather than read from disk because the GUI ships as
// a single binary: files loaded from a path would be missing exactly when
// someone runs it out of a release archive.
//
// It lives here, in main, rather than in internal/ui, for the same reason
// internal/sorint exists on the other side of the repository: the widgets stay
// free of the company, and everything specific to it is in one file.
var (
	//go:embed icon.png
	iconPNG []byte

	// The full logo in its two versions. The lettering is solid white in one
	// and solid black in the other, so the choice is not cosmetic: the wrong
	// one is invisible against its background.
	//
	//go:embed logo-on-dark.svg
	logoOnDarkSVG []byte
	//go:embed logo-on-light.svg
	logoOnLightSVG []byte
)

// branding is the artwork as Fyne wants it.
//
// The resource names matter twice over: Fyne keys its resource cache on them,
// so they have to be stable, and it picks a decoder from the extension, so the
// SVGs have to keep theirs.
var branding = ui.Branding{
	Icon:    fyne.NewStaticResource("sorint.png", iconPNG),
	OnDark:  fyne.NewStaticResource("sorint-on-dark.svg", logoOnDarkSVG),
	OnLight: fyne.NewStaticResource("sorint-on-light.svg", logoOnLightSVG),
}
