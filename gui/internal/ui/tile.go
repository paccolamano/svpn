package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

// tile is one of the three figures under the status: a small caption over a
// large value, on a rounded panel.
//
// It is built from canvas primitives rather than widget.Card because a Card
// brings a title bar, its own padding and a shadow — three decisions that all
// have to be undone to get a plain rounded rectangle.
type tile struct {
	background *canvas.Rectangle
	caption    *canvas.Text
	value      *canvas.Text
	object     fyne.CanvasObject
}

func newTile(caption string) *tile {
	t := &tile{
		background: canvas.NewRectangle(color.Transparent),
		caption:    canvas.NewText(caption, color.Transparent),
		value:      canvas.NewText(absent, color.Transparent),
	}

	t.background.CornerRadius = 10

	t.caption.TextSize = 11
	t.caption.Alignment = fyne.TextAlignCenter

	t.value.TextSize = 15
	t.value.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	t.value.Alignment = fyne.TextAlignCenter

	t.object = container.NewStack(
		t.background,
		container.NewPadded(container.NewVBox(t.caption, t.value)),
	)

	return t
}

// setValue writes the figure.
func (t *tile) setValue(text string) {
	if t.value.Text == text {
		return
	}

	t.value.Text = text
	t.value.Refresh()
}

// applyTheme re-reads the colours.
//
// canvas primitives take a fixed colour at construction rather than following
// the theme the way a widget does, so every repaint sets them again. That is
// also what makes the window survive the desktop switching between light and
// dark while it is open: the next status poll repaints it in the new palette.
func (t *tile) applyTheme() {
	t.background.FillColor = theme.Color(theme.ColorNameInputBackground)
	t.background.Refresh()

	t.caption.Color = theme.Color(theme.ColorNamePlaceHolder)
	t.caption.Refresh()

	t.value.Color = theme.Color(theme.ColorNameForeground)
	t.value.Refresh()
}
