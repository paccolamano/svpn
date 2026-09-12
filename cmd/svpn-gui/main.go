// Command svpn-gui is the desktop client for the svpn VPN.
//
// It is a client of svpnd exactly as the svpn CLI is, and on the same code:
// internal/client reaches the daemon's control socket and internal/client/auth
// reaches the identity provider. It holds no privilege of its own. Everything that needs root — the tun device,
// the routing table, DNS — happens in the daemon, which is why connecting a
// VPN from a window does not have to mean typing a password into one.
package main

import (
	"context"
	"flag"
	"os"

	"fyne.io/fyne/v2/app"

	"github.com/paccolamano/svpn/internal/client"
	"github.com/paccolamano/svpn/internal/client/auth"
	"github.com/paccolamano/svpn/internal/client/gui"
	"github.com/paccolamano/svpn/internal/client/state"
	"github.com/paccolamano/svpn/internal/ipc"
	"github.com/paccolamano/svpn/internal/sorint"
)

func main() {
	socket := flag.String("socket", ipc.DefaultSocket, "daemon control socket")
	host := flag.String("host", "", "gateway hostname; empty uses the built-in default")
	port := flag.Int("port", 0, "gateway port; zero uses the built-in default")
	flag.Parse()

	application := app.NewWithID(sorint.AppID)
	application.SetIcon(branding.Icon)

	// The theme is set here rather than inside gui.New because it is a property
	// of the application, not of one window: menus, tooltips and the tray's
	// own popups are drawn by Fyne outside any window this program owns.
	application.Settings().SetTheme(gui.Theme())

	controller := state.New(
		client.Client{Socket: *socket},
		state.BrowserLogin(auth.Options{Host: *host, Port: *port}),
		state.Options{},
	)

	// The controller outlives nothing: when the window closes, the process
	// goes with it, and the tunnel stays up because it belongs to the daemon.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go controller.Run(ctx)

	window := gui.New(application, controller, branding)

	go window.Watch(controller.Updates())

	window.Run()

	os.Exit(0)
}
