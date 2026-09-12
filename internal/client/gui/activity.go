package gui

import (
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

// activityHeight is how thick the bar is.
//
// widget.ProgressBarInfinite cannot be made this thin. Its minimum height is a
// line of text plus two inner paddings — about forty pixels with this theme —
// and the only way to bring that down is to shrink SizeNameInnerPadding, which
// would take every button and panel in the window with it. Hence the small
// component below.
const activityHeight = 3

// barWidthRatio is how much of the track the moving segment covers, and
// sweepDuration is one pass across it. Slower and wider than a spinner on
// purpose: this runs during a browser login, which lasts as long as the person
// does, and something racing for three minutes reads as a stuck program.
const (
	barWidthRatio = 0.3
	sweepDuration = 1100 * time.Millisecond
)

// activityBar is a slim indeterminate progress indicator.
type activityBar struct {
	track     *canvas.Rectangle
	bar       *canvas.Rectangle
	object    *fyne.Container
	animation *fyne.Animation
	running   bool
}

func newActivityBar() *activityBar {
	a := &activityBar{
		track: canvas.NewRectangle(color.Transparent),
		bar:   canvas.NewRectangle(color.Transparent),
	}

	a.track.CornerRadius = activityHeight / 2.0
	a.bar.CornerRadius = activityHeight / 2.0

	a.object = container.New(activityLayout{bar: a.bar}, a.track, a.bar)
	a.object.Hide()

	a.animation = fyne.NewAnimation(sweepDuration, a.sweep)
	a.animation.Curve = fyne.AnimationEaseInOut
	a.animation.RepeatCount = fyne.AnimationRepeatForever

	return a
}

// setRunning starts or stops the sweep, and shows or hides the bar with it.
func (a *activityBar) setRunning(running bool) {
	if running == a.running {
		return
	}

	a.running = running

	if running {
		a.object.Show()
		a.animation.Start()

		return
	}

	a.animation.Stop()
	a.object.Hide()
}

// applyTheme re-reads the colours. canvas primitives take a fixed colour at
// construction rather than following the theme the way a widget does.
func (a *activityBar) applyTheme() {
	a.track.FillColor = theme.Color(theme.ColorNameSeparator)
	a.track.Refresh()

	a.bar.FillColor = theme.Color(theme.ColorNamePrimary)
	a.bar.Refresh()
}

// sweep places the bar for one frame.
//
// It starts wholly off the left edge and ends wholly past the right, so the
// two ends of the loop meet on empty track. Travelling only edge to edge would
// park the segment against a side for a moment at each turn, which reads as
// the animation catching rather than repeating.
func (a *activityBar) sweep(done float32) {
	width := a.object.Size().Width
	if width <= 0 {
		return
	}

	barWidth := width * barWidthRatio

	a.bar.Move(fyne.NewPos((width+barWidth)*done-barWidth, 0))
}

// activityLayout stretches the track across the whole width and gives the bar
// its height, leaving the bar's horizontal position to the animation — which
// is why this cannot be one of the stock layouts.
type activityLayout struct{ bar *canvas.Rectangle }

func (l activityLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, activityHeight)
}

func (l activityLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	track := objects[0]
	track.Resize(size)
	track.Move(fyne.NewPos(0, 0))

	l.bar.Resize(fyne.NewSize(size.Width*barWidthRatio, size.Height))
}
