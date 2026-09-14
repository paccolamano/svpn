package gui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/paccolamano/svpn/internal/client/state"
	"github.com/paccolamano/svpn/internal/ipc"
)

// captureDir names a directory to write each rendered phase into as a PNG.
//
// It exists because most of what this window does cannot be reached by hand on
// the machine it is developed on: the browser login needs an identity
// provider, and the unreachable-daemon panel needs the service to be missing.
// Rendering them through Fyne's test driver is the only way to look at them
// without arranging the world around each one.
const captureDir = "SVPN_UI_CAPTURE_DIR"

// renderCases is one snapshot per phase the window has to draw.
func renderCases() map[string]state.Snapshot {
	return map[string]state.Snapshot{
		"connected": {
			Phase: state.PhaseConnected,
			Status: ipc.Status{
				State:     ipc.StateConnected,
				Gateway:   "vpn.example.it:443",
				Interface: "svpn0",
				LocalIP:   "10.110.248.75",
				DNS:       []string{"10.140.9.4", "10.0.16.26"},
				Routes:    make([]string, 171),
				Since:     time.Now().Add(-102 * time.Minute),
				BytesIn:   125000,
				BytesOut:  77000,
			},
		},
		"disconnected": {
			Phase:  state.PhaseDisconnected,
			Status: ipc.Status{State: ipc.StateDisconnected, Gateway: "vpn.example.it:443"},
		},
		"authenticating": {
			Phase: state.PhaseAuthenticating,
			// A real callback URL is this shape: long, opaque, and not
			// something the window should ever show in full.
			LoginURL: "https://idp.example.it/saml/sso?SAMLRequest=fVLLbtswELzzKwTeJZF6" +
				"WBZiG0iTFA2QtEacHnopaHJlE5BIlUs58d9XkuPWuaTXndmZ2dm9wnbT8pvO" +
				"782zhj8doI_etI3BT8iCds5wJ9CgMaIF5F7y9c3TI09Cxjtnv",
		},
		"no-daemon": {
			Phase: state.PhaseNoDaemon,
			Err:   "sending the status command: connecting to the daemon at /run/svpn/sock: dial unix /run/svpn/sock: connect: no such file or directory",
		},
	}
}

// TestWindowRendersEveryPhase draws each phase through Fyne's test driver.
//
// It asserts nothing about pixels — a golden image here would fail on every
// font or theme change and teach nobody anything. What it does catch is the
// failure this window is actually prone to: a Render path that panics or
// leaves a widget nil for a state nobody reaches by hand.
func TestWindowRendersEveryPhase(t *testing.T) {
	application := test.NewApp()

	application.Settings().SetTheme(Theme())

	branding := Branding{
		Icon:    theme.ComputerIcon(),
		OnLight: theme.ComputerIcon(),
		OnDark:  theme.ComputerIcon(),
	}

	directory := os.Getenv(captureDir)

	for name, snapshot := range renderCases() {
		window := New(application, state.New(nil, nil, state.Options{}), branding)

		window.Render(snapshot)

		picture := window.win.Canvas().Capture()
		if picture == nil {
			t.Fatalf("%s: the window rendered nothing", name)
		}

		if bounds := picture.Bounds(); bounds.Dx() == 0 || bounds.Dy() == 0 {
			t.Fatalf("%s: the window rendered an empty canvas", name)
		}

		if directory != "" {
			writeCapture(t, filepath.Join(directory, name+".png"), picture)
		}

		window.win.Close()
	}
}

// TestFixedSizeSurvivesAPanelAppearing guards the one thing that could have
// broken when the window stopped being resizable.
//
// Fyne only ever grows a fixed window's limits on its own, so a card that has
// to make room for a failure panel and then give the room back depends on the
// explicit Resize in fitToContent. Without it the window would keep the height
// of the worst thing that ever happened to it.
func TestFixedSizeSurvivesAPanelAppearing(t *testing.T) {
	application := test.NewApp()

	application.Settings().SetTheme(Theme())

	window := New(application, state.New(nil, nil, state.Options{}), Branding{
		Icon:    theme.ComputerIcon(),
		OnLight: theme.ComputerIcon(),
		OnDark:  theme.ComputerIcon(),
	})
	defer window.win.Close()

	cases := renderCases()

	window.Render(cases["disconnected"])
	quiet := window.win.Canvas().Size().Height

	window.Render(cases["no-daemon"])
	withPanel := window.win.Canvas().Size().Height

	if withPanel <= quiet {
		t.Fatalf("the window did not grow for the failure panel: %v then %v", quiet, withPanel)
	}

	window.Render(cases["disconnected"])
	if again := window.win.Canvas().Size().Height; again != quiet {
		t.Fatalf("the window kept the failure panel's height: %v, want %v", again, quiet)
	}
}

// writeCapture saves one rendered phase for a human to look at.
func writeCapture(t *testing.T, path string, picture image.Image) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}

	defer func() { _ = file.Close() }()

	if err := png.Encode(file, picture); err != nil {
		t.Fatalf("encoding %s: %v", path, err)
	}
}
