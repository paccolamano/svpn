package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
)

// markSize is the box the logo is drawn into.
//
// The artwork is a wordmark rather than a symbol — about three and a half
// times wider than it is tall — so this has to hold that ratio. It is drawn
// with ImageFillContain, which means getting it wrong leaves air rather than
// stretched lettering, but air in the wrong place is still wrong.
var markSize = fyne.NewSize(208, 61)

// fadedTranslucency is how much of the logo is taken away when there is no
// tunnel.
//
// Dimming rather than recolouring: the logo is a company's, and tinting it
// green or red would be inventing brand colours it does not have. Fading the
// real one says "off" without saying it in the wrong colours.
// Not lower: at seventy per cent the dark lettering on a light background
// washes out to a grey that reads as a rendering fault rather than as a state.
const fadedTranslucency = 0.55

// mark is the logo, in the version that suits the current palette and at the
// intensity that suits the current state.
type mark struct {
	onLight fyne.Resource
	onDark  fyne.Resource
	object  *canvas.Image
	shown   fyne.Resource
}

func newMark(branding Branding) *mark {
	m := &mark{onLight: branding.OnLight, onDark: branding.OnDark, shown: branding.OnDark}

	m.object = canvas.NewImageFromResource(m.shown)
	m.object.FillMode = canvas.ImageFillContain

	return m
}

// canvasObject is the logo laid out at its fixed size. A bare canvas.Image has
// no minimum size of its own, so in a vertical box it would collapse to
// nothing.
func (m *mark) canvasObject() fyne.CanvasObject {
	return container.NewGridWrap(markSize, m.object)
}

// apply picks the artwork for the palette and dims it when there is no tunnel.
func (m *mark) apply(variant fyne.ThemeVariant, connected bool) {
	wanted := m.onDark
	if variant == theme.VariantLight {
		wanted = m.onLight
	}

	translucency := fadedTranslucency
	if connected {
		translucency = 0
	}

	if m.shown == wanted && m.object.Translucency == translucency {
		return
	}

	m.shown = wanted
	m.object.Resource = wanted
	m.object.Translucency = translucency
	m.object.Refresh()
}
