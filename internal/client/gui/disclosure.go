package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// disclosure is a row that reveals a panel underneath it.
//
// It is hand-rolled rather than a widget.Accordion because the window sizes
// itself to whatever it holds, and Accordion offers no way of being told that
// it opened — only a public field to poll, which would leave the window a
// second behind the click that should have resized it.
type disclosure struct {
	button *widget.Button
	detail fyne.CanvasObject
	object *fyne.Container
	open   bool
	// onToggle lets the window resize to the new shape.
	onToggle func()
}

func newDisclosure(title string, detail fyne.CanvasObject, onToggle func()) *disclosure {
	d := &disclosure{detail: detail, onToggle: onToggle}

	d.button = widget.NewButtonWithIcon(title, theme.MenuDropDownIcon(), d.toggle)
	d.button.Alignment = widget.ButtonAlignLeading
	d.button.Importance = widget.LowImportance

	d.detail.Hide()
	d.object = container.NewVBox(d.button, d.detail)

	return d
}

func (d *disclosure) toggle() {
	d.open = !d.open

	if d.open {
		d.button.SetIcon(theme.MenuDropUpIcon())
		d.detail.Show()
	} else {
		d.button.SetIcon(theme.MenuDropDownIcon())
		d.detail.Hide()
	}

	if d.onToggle != nil {
		d.onToggle()
	}
}
