package ui

import (
	"image/color"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/paccolamano/svpn/gui/internal/core"
)

// windowWidth is the card's narrowest useful width; the height is never fixed
// at all. Both are settled by fitToContent, so that adding a row cannot leave
// a band of empty space under the button.
//
// It is deliberately narrow. This is a status card someone opens from the
// tray, reads in a second and closes, not a console they keep open.
const windowWidth = 380

// gutter is the space between the card's contents and the window's edge.
//
// It is wider than the theme's own padding on purpose: at the theme value the
// outermost things — the row of figures, the button — sat against the frame
// with nothing between them and whatever the window manager draws there.
const gutter = 14

// Branding is the artwork the window is dressed with.
//
// It is passed in rather than embedded here so that nothing in this package
// knows whose VPN this is — the same separation internal/sorint keeps on the
// other side of the repository.
type Branding struct {
	// Icon is the square mark, for the window and the notification area. A
	// wordmark is unreadable at the size a tray gives it.
	Icon fyne.Resource
	// OnLight and OnDark are the full logo. A logo with lettering needs both:
	// the text is solid in one colour, so one file is invisible against the
	// background the other is made for.
	OnLight fyne.Resource
	OnDark  fyne.Resource
}

// shape is the set of optional panels on screen.
//
// The window is resized when this changes and at no other time, so that it
// does not fight a user who has dragged it to a size of their own once a
// second.
type shape struct {
	login   bool
	failure bool
	busy    bool
}

// Window is the client's only window.
//
// Every widget it writes into later is held here rather than rebuilt: a
// container rebuilt on each update loses focus, scroll position and whatever
// the user had selected, once a second.
type Window struct {
	app        fyne.App
	win        fyne.Window
	controller *core.Controller

	mark     *mark
	headline *canvas.Text
	gateway  *canvas.Text
	hint     *widget.Label

	activity *activityBar

	loginBackground *canvas.Rectangle
	loginText       *widget.RichText
	loginLink       *widget.HyperlinkSegment
	loginBox        *fyne.Container
	// loginShown is the URL the link currently points at, so that a poll that
	// changes nothing does not rebuild the text once a second.
	loginShown string

	received *tile
	sent     *tile
	uptime   *tile

	iface   *widget.Label
	address *widget.Label
	dns     *widget.Label
	routes  *widget.Label
	details *disclosure

	failureBackground *canvas.Rectangle
	failure           *widget.Label
	failureBox        *fyne.Container

	action *widget.Button
	cancel *widget.Button

	trayToggle *fyne.MenuItem
	trayMenu   *fyne.Menu

	branding Branding

	// painted is the foreground colour the canvas primitives were last drawn
	// with. Those do not follow the theme the way a widget does, so they are
	// repainted by hand — but only when the palette has actually changed,
	// rather than on all nine of them once a second.
	painted color.Color

	// last is what is on screen, so that the tray menu and the one button can
	// act on the same state the user is looking at.
	last core.Snapshot
	// shown is the set of panels the window was last sized for.
	shown shape
}

// New builds the window. It is not shown until Run is called.
func New(application fyne.App, controller *core.Controller, branding Branding) *Window {
	w := &Window{
		app:        application,
		win:        application.NewWindow("svpn"),
		controller: controller,
		branding:   branding,
	}

	w.build()
	w.installTray()

	w.win.SetContent(w.content())
	w.win.SetIcon(branding.Icon)
	w.fitToContent()

	// Not resizable by hand. Every size in here is derived from the content,
	// so there is nothing a user could gain by dragging an edge: the card
	// would keep its layout and grow a margin. fitToContent still resizes it
	// from code, which is what a fixed-size window in Fyne means — the limits
	// follow the last requested size rather than being frozen at the first.
	w.win.SetFixedSize(true)

	return w
}

// Run shows the window and blocks until the application exits.
func (w *Window) Run() { w.win.ShowAndRun() }

// Watch renders snapshots as they arrive.
//
// Every widget write goes through fyne.Do: Fyne owns its main thread, and
// touching a widget from another goroutine is a race that surfaces as a torn
// frame or a crash long after the call that caused it.
func (w *Window) Watch(updates <-chan core.Snapshot) {
	for snapshot := range updates {
		fyne.Do(func() { w.Render(snapshot) })
	}
}

// build creates every widget.
func (w *Window) build() {
	w.mark = newMark(w.branding)

	w.headline = canvas.NewText("", color.Transparent)
	w.headline.TextSize = 21
	w.headline.TextStyle = fyne.TextStyle{Bold: true}
	w.headline.Alignment = fyne.TextAlignCenter

	w.gateway = canvas.NewText("", color.Transparent)
	w.gateway.TextSize = 12
	w.gateway.Alignment = fyne.TextAlignCenter

	w.hint = widget.NewLabel("")
	w.hint.Alignment = fyne.TextAlignCenter
	w.hint.Wrapping = fyne.TextWrapWord
	w.hint.Hide()

	w.activity = newActivityBar()

	w.buildLogin()
	w.buildTiles()
	w.buildDetails()
	w.buildFailure()

	w.action = widget.NewButton("Connect", w.onAction)
	w.action.Importance = widget.HighImportance

	w.cancel = widget.NewButton("Cancel", w.controller.Cancel)
	w.cancel.Importance = widget.LowImportance
	w.cancel.Hide()
}

// buildLogin makes the panel that shows where the identity provider is
// waiting. The link is not decoration: when the browser fails to open — no
// default handler, a locked-down desktop — it is the only way through.
func (w *Window) buildLogin() {
	w.loginBackground = canvas.NewRectangle(color.Transparent)
	w.loginBackground.CornerRadius = 10

	// "click here" rather than the address itself. A SAML callback URL is a
	// hundred-odd characters of opaque query string: shown in full it wrapped
	// over four lines and took over the window, and nobody reads one — they
	// only click it.
	w.loginLink = &widget.HyperlinkSegment{Text: "click here", Alignment: fyne.TextAlignLeading}

	w.loginText = widget.NewRichText(
		&widget.TextSegment{
			// Short enough to stay on one line at this width. The longer
			// phrasing wrapped, and a wrap that falls inside "click here"
			// splits the only thing on the panel worth clicking.
			Text:  "If your browser did not open, ",
			Style: widget.RichTextStyle{Inline: true, ColorName: theme.ColorNamePlaceHolder},
		},
		w.loginLink,
	)
	w.loginText.Wrapping = fyne.TextWrapWord

	w.loginBox = container.NewStack(
		w.loginBackground,
		container.NewPadded(w.loginText),
	)
	w.loginBox.Hide()
}

func (w *Window) buildTiles() {
	w.received = newTile("received")
	w.sent = newTile("sent")
	w.uptime = newTile("uptime")
}

// buildDetails puts the technical readout behind a disclosure. It is what
// someone opens when something is wrong; leaving it open would make the window
// twice as tall for a question nobody is asking most of the time.
func (w *Window) buildDetails() {
	w.iface = detailValue()
	w.address = detailValue()
	w.dns = detailValue()
	w.routes = detailValue()

	grid := container.New(layout.NewFormLayout(),
		detailLabel("Interface"), w.iface,
		detailLabel("Address"), w.address,
		detailLabel("DNS"), w.dns,
		detailLabel("Routes"), w.routes,
	)

	w.details = newDisclosure("Details", grid, w.fitToContent)
}

func (w *Window) buildFailure() {
	w.failureBackground = canvas.NewRectangle(color.Transparent)
	w.failureBackground.CornerRadius = 10

	w.failure = widget.NewLabel("")
	w.failure.Wrapping = fyne.TextWrapWord

	w.failureBox = container.NewStack(
		w.failureBackground,
		container.NewPadded(w.failure),
	)
	w.failureBox.Hide()
}

// detailLabel is the left-hand cell of the detail table.
func detailLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Importance = widget.LowImportance

	return label
}

// detailValue is the right-hand cell: monospace so that addresses line up and
// do not shift as digits change under them.
func detailValue() *widget.Label {
	label := widget.NewLabel(absent)
	label.TextStyle = fyne.TextStyle{Monospace: true}
	label.Truncation = fyne.TextTruncateEllipsis

	return label
}

// content assembles the layout.
func (w *Window) content() fyne.CanvasObject {
	hero := container.New(
		layout.NewCustomPaddedLayout(gutter, gutter, 0, 0),
		container.NewVBox(
			container.NewCenter(w.mark.canvasObject()),
			w.headline,
			w.gateway,
			w.hint,
		),
	)

	tiles := container.NewGridWithColumns(3,
		w.received.object, w.sent.object, w.uptime.object)

	body := container.NewVBox(
		hero,
		w.activity.object,
		w.loginBox,
		w.failureBox,
		tiles,
		w.details.object,
	)

	footer := container.NewVBox(w.action, w.cancel)

	return container.New(
		layout.NewCustomPaddedLayout(gutter, gutter, gutter, gutter),
		container.NewBorder(nil, footer, nil, nil, body),
	)
}

// fitToContent sizes the window to exactly what it holds.
//
// There is no scroll view here, and this is the reason: Fyne gives a window a
// minimum size equal to its content, so a card that is always its own size can
// never be scrolled at all. That removes both the scroll bar — which sat on
// top of the last figure — and the hard-edged shadow that snapped in above the
// first one, neither of which had a good answer on its own terms.
func (w *Window) fitToContent() {
	padding := theme.Size(theme.SizeNamePadding)
	size := w.win.Content().MinSize()

	w.win.Resize(fyne.NewSize(
		max(windowWidth, size.Width+2*padding),
		size.Height+2*padding,
	))
}

// onAction is the one button: what it does depends on what is on screen, so
// the primary action is always in the same place.
func (w *Window) onAction() {
	switch {
	case w.last.CanDisconnect():
		w.controller.Disconnect()
	case w.last.CanConnect():
		w.controller.Connect()
	case w.last.Phase == core.PhaseNoDaemon:
		w.controller.Refresh()
	}
}

// Render puts a snapshot on screen. It must run on the Fyne main thread.
func (w *Window) Render(snapshot core.Snapshot) {
	w.last = snapshot
	status := snapshot.Status

	w.applyTheme()

	setCanvasText(w.headline, headline(snapshot.Phase),
		theme.Color(statusColorName(snapshot.Phase)))
	// The gateway line disappears rather than showing a dash: in the detail
	// table a dash lines up with the other empty cells and reads as "nothing
	// here yet", but alone under the headline it reads as a glyph that failed
	// to render.
	setCanvasText(w.gateway, status.Gateway, theme.Color(theme.ColorNamePlaceHolder))

	setText(w.hint, hint(snapshot))

	connected := snapshot.Phase == core.PhaseConnected
	w.mark.apply(w.app.Settings().ThemeVariant(), connected)

	w.received.setValue(whileConnected(connected, formatBytes(status.BytesIn)))
	w.sent.setValue(whileConnected(connected, formatBytes(status.BytesOut)))
	w.uptime.setValue(whileConnected(connected, formatSince(status.Since, time.Now())))

	w.iface.SetText(orAbsent(status.Interface))
	w.address.SetText(orAbsent(status.LocalIP))
	w.dns.SetText(formatList(status.DNS, 2))
	w.routes.SetText(countOrAbsent(len(status.Routes)))

	failure := firstNonEmpty(snapshot.Err, status.LastError)

	w.renderFailure(failure)
	w.renderLogin(snapshot)
	w.renderActions(snapshot)
	w.renderTray(snapshot)

	// Resizing only when the set of panels changed keeps the window still
	// while the figures tick over, which is almost all of the time.
	current := shape{
		login:   snapshot.LoginURL != "",
		failure: failure != "",
		busy:    snapshot.Busy(),
	}
	if current != w.shown {
		w.shown = current

		w.fitToContent()
	}
}

// applyTheme repaints the canvas primitives, which do not follow the theme on
// their own. It is a no-op unless the palette has changed, so the desktop
// switching to dark while the window is open is picked up within a second
// without repainting everything the rest of the time.
func (w *Window) applyTheme() {
	foreground := theme.Color(theme.ColorNameForeground)
	if w.painted == foreground {
		return
	}

	w.painted = foreground

	for _, t := range []*tile{w.received, w.sent, w.uptime} {
		t.applyTheme()
	}

	w.failureBackground.FillColor = withAlpha(theme.Color(theme.ColorNameError), 0x1F)
	w.failureBackground.Refresh()

	w.loginBackground.FillColor = withAlpha(theme.Color(theme.ColorNamePrimary), 0x1F)
	w.loginBackground.Refresh()

	w.activity.applyTheme()
}

// renderFailure shows the daemon's account of a failure and this side's in the
// same place: to the user they are one question, "why did it not work".
func (w *Window) renderFailure(message string) {
	if message == "" {
		w.failureBox.Hide()

		return
	}

	w.failure.SetText(message)
	w.failureBox.Show()
}

// renderLogin shows the authentication URL while the browser round trip is
// outstanding.
func (w *Window) renderLogin(snapshot core.Snapshot) {
	if snapshot.LoginURL == "" {
		w.loginBox.Hide()

		return
	}

	if w.loginShown != snapshot.LoginURL {
		w.loginShown = snapshot.LoginURL

		// An address that will not parse leaves the link inert rather than
		// hiding the prompt: the sentence about the browser is the useful half
		// either way, and a dead link is a smaller failure than a blank panel.
		parsed, err := url.Parse(snapshot.LoginURL)
		if err != nil {
			parsed = nil
		}

		w.loginLink.URL = parsed
		w.loginText.Refresh()
	}

	w.loginBox.Show()
}

// renderActions decides what the buttons say and whether they do anything.
func (w *Window) renderActions(snapshot core.Snapshot) {
	switch {
	case snapshot.CanDisconnect():
		w.action.SetText("Disconnect")
		w.action.Importance = widget.DangerImportance
		w.action.Enable()
	case snapshot.Phase == core.PhaseNoDaemon:
		w.action.SetText("Try again")
		w.action.Importance = widget.MediumImportance
		w.action.Enable()
	default:
		w.action.SetText("Connect")
		w.action.Importance = widget.HighImportance

		if snapshot.CanConnect() {
			w.action.Enable()
		} else {
			w.action.Disable()
		}
	}

	w.action.Refresh()

	// Only the browser login is worth offering to abandon; it is the one that
	// lasts as long as a person does.
	if snapshot.Phase == core.PhaseAuthenticating {
		w.cancel.Show()
	} else {
		w.cancel.Hide()
	}

	w.activity.setRunning(snapshot.Busy())
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
	desk.SetSystemTrayIcon(w.branding.Icon)

	w.win.SetCloseIntercept(func() { w.win.Hide() })
}

// renderTray keeps the tray menu saying what the window says.
func (w *Window) renderTray(snapshot core.Snapshot) {
	if w.trayToggle == nil {
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
}

// statusColorName is the whole status readout for anyone glancing at the
// window from across a desk, so it says one of four things and no more.
//
// It names a theme colour rather than resolving one: theme.Color reads the
// running application's theme and panics when there is none, which would make
// this — the one piece of rendering that is pure logic — testable only with a
// live Fyne app behind it.
func statusColorName(phase core.Phase) fyne.ThemeColorName {
	switch phase {
	case core.PhaseConnected:
		return theme.ColorNameSuccess
	case core.PhaseNoDaemon:
		return theme.ColorNameError
	case core.PhaseConnecting, core.PhaseAuthenticating, core.PhaseDisconnecting:
		return theme.ColorNameWarning
	case core.PhaseDisconnected:
		// Not an alarm and not an achievement: there is simply no tunnel. A
		// coloured word here would make the ordinary state look like an event.
		return theme.ColorNameForeground
	case core.PhaseUnknown:
		return theme.ColorNamePlaceHolder
	default:
		return theme.ColorNamePlaceHolder
	}
}

// setCanvasText writes a canvas text only when something about it changed. A
// canvas primitive has no dirty tracking of its own, so every Refresh is a
// repaint whether or not anything moved.
func setCanvasText(target *canvas.Text, text string, colour color.Color) {
	if target.Text == text && target.Color == colour {
		return
	}

	target.Text = text
	target.Color = colour
	target.Refresh()

	// Hidden rather than blank: a vertical box leaves no room for what is
	// hidden, so an empty line closes up instead of leaving a gap.
	if text == "" {
		target.Hide()
	} else {
		target.Show()
	}
}

// setText writes a label and hides it when there is nothing to say, so an
// empty one does not leave a gap the layout has to explain.
func setText(label *widget.Label, text string) {
	label.SetText(text)

	if text == "" {
		label.Hide()
	} else {
		label.Show()
	}
}

// withAlpha rebuilds a colour at a given opacity, for the tinted panels.
//
// It goes through NRGBAModel rather than shifting the values RGBA returns:
// those are premultiplied, so re-using them straight would darken any source
// colour that was not already opaque.
func withAlpha(source color.Color, alpha uint8) color.NRGBA {
	converted, ok := color.NRGBAModel.Convert(source).(color.NRGBA)
	if !ok {
		return color.NRGBA{A: alpha}
	}

	converted.A = alpha

	return converted
}

// firstNonEmpty returns the first value with something in it.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

// countOrAbsent renders zero as absent: no routes and "0 routes" mean the same
// thing, and the dash matches every other empty cell.
func countOrAbsent(count int) string {
	if count == 0 {
		return absent
	}

	return formatCount(count)
}
