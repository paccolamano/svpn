package main

import (
	"fyne.io/fyne/v2"

	"github.com/paccolamano/svpn/internal/client/gui"
	"github.com/paccolamano/svpn/internal/sorint"
)

// branding is internal/sorint's artwork as Fyne wants it.
//
// The wrapping happens here, in main, rather than in internal/sorint, because
// svpnd imports that package too — for the icon it writes out at install time
// — and it must not acquire a dependency on an OpenGL toolchain to do it.
//
// The resource names matter twice over: Fyne keys its resource cache on them,
// so they have to be stable, and it picks a decoder from the extension, so the
// SVGs have to keep theirs.
var branding = gui.Branding{
	Icon:    fyne.NewStaticResource("sorint.png", sorint.Icon),
	OnDark:  fyne.NewStaticResource("sorint-on-dark.svg", sorint.LogoOnDark),
	OnLight: fyne.NewStaticResource("sorint-on-light.svg", sorint.LogoOnLight),
}
