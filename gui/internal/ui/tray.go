package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/paccolamano/svpn/gui/internal/core"
)

// The chip's geometry, as fractions of the icon's shorter side. The artwork is
// 212 pixels square and a panel draws it at around twenty, so anything
// expressed in pixels here would be wrong for one of those two sizes.
//
// The diameter is a compromise found by rendering the real mark at both sizes:
// much smaller and a panel scales the dot down to a handful of pixels, too few
// to carry a colour reliably; much larger and it eats the bottom half of the
// mark everywhere the icon is shown at its full size.
const (
	chipDiameter = 0.40
	chipInset    = 0.02
	// chipMoat is a ring cleared around the chip so it does not merge into the
	// mark underneath. It is cleared rather than painted: the panel behind a
	// tray icon can be any colour, and a ring in this window's background
	// would be a pale halo on a dark panel and a dark one on a pale panel.
	chipMoat = 0.045
)

// Three colours and no more. The chip answers one question — am I on the VPN —
// and a palette with a fourth entry turns it into something to decode rather
// than something to read. A grey one was the fourth: against a light panel, at
// the size a panel draws this, it was hard to tell from the green.
//
// They are fixed rather than read from the theme. The theme describes this
// window; the chip is drawn onto a desktop panel whose colours nothing here
// can see. So these are mid-tones — each has to stay itself against a black
// panel and a white one, which is why neither the light palette's deep green
// nor the dark palette's pale red is used.
var (
	chipConnected = color.NRGBA{R: 0x2E, G: 0xA0, B: 0x43, A: 0xFF}
	chipBusy      = color.NRGBA{R: 0xD2, G: 0x99, B: 0x22, A: 0xFF}
	chipOffline   = color.NRGBA{R: 0xDA, G: 0x36, B: 0x33, A: 0xFF}
)

// chipColor is the dot a phase wears, and whether it wears one at all.
//
// The window separates "no tunnel" from "no daemon" because it has the room to
// say what to do about each. Out here both are red: there is no tunnel either
// way, which is the only thing a dot can tell anyone.
//
// The window's own headline leaves an ordinary disconnected state uncoloured,
// so that the quiet case does not read as an event. The chip cannot afford
// that: it is alone on a panel with nothing to qualify it, and a state that
// says nothing there is indistinguishable from one that failed to draw.
//
// PhaseUnknown is the exception, and has no chip: it lasts until the first
// status reply, and a red dot there would answer the question before the
// daemon has been asked.
func chipColor(phase core.Phase) (color.NRGBA, bool) {
	switch phase {
	case core.PhaseConnected:
		return chipConnected, true
	case core.PhaseConnecting, core.PhaseAuthenticating, core.PhaseDisconnecting:
		return chipBusy, true
	case core.PhaseDisconnected, core.PhaseNoDaemon:
		return chipOffline, true
	case core.PhaseUnknown:
		return color.NRGBA{}, false
	default:
		return color.NRGBA{}, false
	}
}

// trayIcons is the tray artwork in each of its states.
//
// They are rendered once and kept because a snapshot arrives every second
// whether or not anything changed, and each render decodes, composites and
// re-encodes a PNG.
type trayIcons struct {
	base fyne.Resource
	// source is the decoded artwork, or nil when it could not be decoded —
	// which is not hypothetical: Fyne resources are just as often SVG, and the
	// tests dress this window in one of the stock icons.
	source image.Image
	cache  map[core.Phase]fyne.Resource
}

func newTrayIcons(base fyne.Resource) *trayIcons {
	icons := &trayIcons{base: base, cache: make(map[core.Phase]fyne.Resource)}

	if decoded, _, err := image.Decode(bytes.NewReader(base.Content())); err == nil {
		icons.source = decoded
	}

	return icons
}

// forPhase is the icon to show for a phase. It falls back to the undecorated
// artwork rather than to nothing: an empty tray icon would be a worse answer
// to "am I connected" than no chip at all.
func (t *trayIcons) forPhase(phase core.Phase) fyne.Resource {
	if resource, ok := t.cache[phase]; ok {
		return resource
	}

	resource := t.render(phase)
	t.cache[phase] = resource

	return resource
}

func (t *trayIcons) render(phase core.Phase) fyne.Resource {
	fill, chipped := chipColor(phase)
	if !chipped || t.source == nil {
		return t.base
	}

	encoded := &bytes.Buffer{}
	if err := png.Encode(encoded, withChip(t.source, fill)); err != nil {
		return t.base
	}

	// One name per phase, and stable across the life of the process: Fyne keys
	// its resource cache on the name, so two different pictures sharing one
	// would leave whichever was drawn first on screen forever.
	return fyne.NewStaticResource("svpn-tray-"+string(phase)+".png", encoded.Bytes())
}

// withChip draws the status dot into the icon's bottom-right corner.
//
// The result is NRGBA because that is what a tray wants at the far end — the
// unix path re-encodes anything else, and premultiplied alpha would have to be
// undone first to composite a translucent edge correctly.
func withChip(source image.Image, fill color.NRGBA) image.Image {
	bounds := source.Bounds()

	badged := image.NewNRGBA(bounds)
	draw.Draw(badged, bounds, source, bounds.Min, draw.Src)

	side := math.Min(float64(bounds.Dx()), float64(bounds.Dy()))
	radius := side * chipDiameter / 2
	moat := side * chipMoat

	centreX := float64(bounds.Max.X) - side*chipInset - radius
	centreY := float64(bounds.Max.Y) - side*chipInset - radius

	// Only the square the chip and its moat can reach is touched; the rest of
	// the mark is left exactly as the artwork drew it.
	reach := radius + moat + 1
	touched := image.Rect(
		int(math.Floor(centreX-reach)), int(math.Floor(centreY-reach)),
		int(math.Ceil(centreX+reach)), int(math.Ceil(centreY+reach)),
	).Intersect(bounds)

	for y := touched.Min.Y; y < touched.Max.Y; y++ {
		for x := touched.Min.X; x < touched.Max.X; x++ {
			// From the centre of the pixel rather than its corner, so the two
			// discs stay concentric with each other as they are sampled.
			distance := math.Hypot(float64(x)+0.5-centreX, float64(y)+0.5-centreY)

			if cleared := coverage(distance, radius+moat); cleared > 0 {
				pixel := badged.NRGBAAt(x, y)
				pixel.A = uint8(float64(pixel.A)*(1-cleared) + 0.5)

				badged.SetNRGBA(x, y, pixel)
			}

			// The moat has already emptied everything the chip covers, so the
			// dot is written over transparency and its soft edge needs no
			// blending with what was underneath.
			if painted := coverage(distance, radius); painted > 0 {
				badged.SetNRGBA(x, y, color.NRGBA{
					R: fill.R, G: fill.G, B: fill.B,
					A: uint8(float64(fill.A)*painted + 0.5),
				})
			}
		}
	}

	return badged
}

// coverage is how much of a pixel at a given distance falls inside a disc.
//
// It is a one-pixel ramp across the edge rather than a test of the centre: a
// hard-edged circle of ninety pixels scaled down to twenty by the panel comes
// out visibly notched, and the notches read as a damaged icon.
func coverage(distance, radius float64) float64 {
	return math.Min(math.Max(radius+0.5-distance, 0), 1)
}

// installTray puts the client in the notification area, which is where a VPN
// client spends almost all of its life. Closing the window hides it instead of
// quitting, so the tunnel is not torn down by someone tidying their desktop.
func (w *Window) installTray() {
	desk, ok := w.app.(desktop.App)
	if !ok {
		// No tray: a plain desktop, or a session whose panel has no status
		// area. Closing the window then has to mean quitting, or there would
		// be no way back to it.
		return
	}

	w.tray = desk
	w.trayIcons = newTrayIcons(w.branding.Icon)

	w.trayToggle = fyne.NewMenuItem("Connect", w.onAction)

	w.trayMenu = fyne.NewMenu("svpn",
		fyne.NewMenuItem("Show", func() {
			w.win.Show()
			w.win.RequestFocus()
		}),
		fyne.NewMenuItemSeparator(),
		w.trayToggle,
	)

	desk.SetSystemTrayMenu(w.trayMenu)
	w.setTrayIcon(core.PhaseUnknown)

	w.win.SetCloseIntercept(func() { w.win.Hide() })
}

// renderTray keeps the tray saying what the window says — in the menu, and in
// the chip on the icon, which is the whole of what someone who has not opened
// either gets to see.
func (w *Window) renderTray(snapshot core.Snapshot) {
	if w.tray == nil {
		return
	}

	switch {
	case snapshot.CanDisconnect():
		w.trayToggle.Label = "Disconnect"
		w.trayToggle.Disabled = false
	case snapshot.CanConnect():
		w.trayToggle.Label = "Connect"
		w.trayToggle.Disabled = false
	default:
		w.trayToggle.Label = headline(snapshot.Phase)
		w.trayToggle.Disabled = true
	}

	w.trayMenu.Refresh()

	w.setTrayIcon(snapshot.Phase)
}

// setTrayIcon hands the panel a new icon only when it is a different one.
//
// Every call re-encodes the image for the platform and, on Linux, publishes a
// pixmap over D-Bus and signals every watcher that the icon changed. Doing
// that once a second for an icon that has not moved is work the desktop pays
// for, not this process.
func (w *Window) setTrayIcon(phase core.Phase) {
	icon := w.trayIcons.forPhase(phase)
	if icon == w.trayShown {
		return
	}

	w.trayShown = icon

	w.tray.SetSystemTrayIcon(icon)
}
