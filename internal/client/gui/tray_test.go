package gui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/paccolamano/svpn/internal/client/state"
)

// trayPhases is every phase the icon has to have an answer for.
var trayPhases = []state.Phase{
	state.PhaseUnknown,
	state.PhaseNoDaemon,
	state.PhaseDisconnected,
	state.PhaseAuthenticating,
	state.PhaseConnecting,
	state.PhaseConnected,
	state.PhaseDisconnecting,
}

// artwork is a stand-in for the real mark: opaque, so that a cleared moat
// shows up as a change rather than as two transparent pixels in a row.
func artwork(t *testing.T) fyne.Resource {
	t.Helper()

	picture := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range picture.Pix {
		picture.Pix[i] = 0xFF
	}

	encoded := &bytes.Buffer{}
	if err := png.Encode(encoded, picture); err != nil {
		t.Fatalf("encoding the test artwork: %v", err)
	}

	return fyne.NewStaticResource("artwork.png", encoded.Bytes())
}

// TestTrayIconCarriesAChipPerPhase is the point of the whole file: someone who
// never opens the window should be able to tell the states apart, which they
// cannot if two of them produce the same picture.
func TestTrayIconCarriesAChipPerPhase(t *testing.T) {
	icons := newTrayIcons(artwork(t))

	// Keyed by the picture, so that two phases drawing the same bytes land in
	// one entry however they got there — the images themselves are never
	// printed, a megabyte of PNG in a failure message helps nobody.
	seen := make(map[string][]state.Phase)

	for _, phase := range trayPhases {
		resource := icons.forPhase(phase)
		if resource == nil {
			t.Fatalf("%s: no tray icon at all", phase)
		}

		if _, _, err := image.Decode(bytes.NewReader(resource.Content())); err != nil {
			t.Fatalf("%s: the tray icon is not a decodable image: %v", phase, err)
		}

		seen[string(resource.Content())] = append(seen[string(resource.Content())], phase)
	}

	// Four pictures for seven phases: the three busy ones share a chip because
	// they are one wait to the person watching, "no tunnel" and "no daemon"
	// share one because they are one answer, and the phase before the first
	// status reply wears none at all.
	const distinct = 4

	if len(seen) != distinct {
		grouped := make([][]state.Phase, 0, len(seen))
		for _, phases := range seen {
			grouped = append(grouped, phases)
		}

		t.Fatalf("the tray draws %d distinct icons, want %d: %v", len(seen), distinct, grouped)
	}
}

// TestTrayIconIsRenderedOnce guards the cache: the snapshot behind this arrives
// once a second, and compositing and re-encoding a PNG that often would be a
// steady cost for a picture that changes a handful of times a day.
func TestTrayIconIsRenderedOnce(t *testing.T) {
	icons := newTrayIcons(artwork(t))

	first := icons.forPhase(state.PhaseConnected)
	if again := icons.forPhase(state.PhaseConnected); again != first {
		t.Fatal("the connected tray icon was rendered a second time")
	}
}

// TestTrayIconFallsBackToTheArtwork covers the resource this cannot decode.
// The stock Fyne icons are SVG, so this is the shape every test in this
// package that dresses a window hits — and an icon that failed to render would
// leave the tray empty, which is worse than one with no chip.
func TestTrayIconFallsBackToTheArtwork(t *testing.T) {
	base := theme.ComputerIcon()

	icons := newTrayIcons(base)

	if resource := icons.forPhase(state.PhaseConnected); resource != base {
		t.Fatalf("an undecodable icon was replaced rather than kept: %v", resource.Name())
	}
}

// TestTrayChipLeavesTheMarkAlone checks the chip stays in its corner. The
// moat clears pixels, so a geometry mistake here does not fail loudly — it
// quietly eats the artwork.
func TestTrayChipLeavesTheMarkAlone(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range source.Pix {
		source.Pix[i] = 0xFF
	}

	badged, ok := withChip(source, chipConnected).(*image.NRGBA)
	if !ok {
		t.Fatal("the chip was not drawn onto an NRGBA image")
	}

	if corner := badged.NRGBAAt(2, 2); corner.A != 0xFF {
		t.Fatalf("the chip reached the opposite corner: %v", corner)
	}

	side := float64(badged.Bounds().Dx())

	offset := int(side * (1 - chipInset - chipDiameter/2))

	centre := badged.NRGBAAt(offset, offset)
	if centre.R != chipConnected.R || centre.G != chipConnected.G || centre.B != chipConnected.B {
		t.Fatalf("the chip is not where it was aimed: %v", centre)
	}
}

// TestTrayCapture writes the real artwork with each chip for a human to look
// at, for the same reason the window's own test does: a tray icon is judged at
// twenty pixels on a panel, and nothing asserted here can tell you whether the
// dot is legible there.
func TestTrayCapture(t *testing.T) {
	directory := os.Getenv(captureDir)
	if directory == "" {
		t.Skip("set " + captureDir + " to write the tray icons out")
	}

	content, err := os.ReadFile(filepath.Join("..", "..", "icon.png"))
	if err != nil {
		t.Skipf("reading the artwork: %v", err)
	}

	icons := newTrayIcons(fyne.NewStaticResource("icon.png", content))

	for _, phase := range trayPhases {
		picture, _, err := image.Decode(bytes.NewReader(icons.forPhase(phase).Content()))
		if err != nil {
			t.Fatalf("%s: decoding the tray icon: %v", phase, err)
		}

		writeCapture(t, filepath.Join(directory, "tray-"+string(phase)+".png"), picture)
	}
}
