// Command svpn-gui is the desktop client for the svpn VPN.
//
// It is a client of svpnd exactly as the svpn CLI is: it speaks pkg/ipc over
// the daemon's control socket and pkg/auth to the identity provider, and it
// holds no privilege of its own. Everything that needs root — the tun device,
// the routing table, DNS — happens in the daemon, which is why connecting a
// VPN from a window does not have to mean typing a password into one.
package main

import (
	"context"
	"flag"
	"os"

	"fyne.io/fyne/v2/app"

	"github.com/paccolamano/svpn/gui/internal/core"
	"github.com/paccolamano/svpn/gui/internal/ui"
	"github.com/paccolamano/svpn/pkg/auth"
	"github.com/paccolamano/svpn/pkg/ipc"
)

// appID names the application to the desktop: it is what the preferences file
// is keyed on and what a notification is attributed to. It has to stay stable
// across releases.
const appID = "it.sorint.svpn"

func main() {
	socket := flag.String("socket", ipc.DefaultSocket, "daemon control socket")
	host := flag.String("host", "", "gateway hostname; empty uses the built-in default")
	port := flag.Int("port", 0, "gateway port; zero uses the built-in default")
	flag.Parse()

	application := app.NewWithID(appID)
	application.SetIcon(branding.Icon)

	// The theme is set here rather than inside ui.New because it is a property
	// of the application, not of one window: menus, tooltips and the tray's
	// own popups are drawn by Fyne outside any window this program owns.
	application.Settings().SetTheme(ui.Theme())

	controller := core.New(
		core.SocketDaemon{Path: *socket},
		core.BrowserLogin(auth.Options{Host: *host, Port: *port}),
		core.Options{},
	)

	// The controller outlives nothing: when the window closes, the process
	// goes with it, and the tunnel stays up because it belongs to the daemon.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go controller.Run(ctx)

	window := ui.New(application, controller, branding)

	go window.Watch(controller.Updates())

	window.Run()

	os.Exit(0)
}
