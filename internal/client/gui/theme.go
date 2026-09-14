package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// brand is the Sorint.LAB orange, sampled from the mark the window carries so
// that the accent and the logo cannot drift apart.
var brand = color.NRGBA{R: 0xEE, G: 0x73, B: 0x30, A: 0xFF}

// tint returns the brand colour at a fraction of its opacity.
func tint(alpha uint8) color.NRGBA {
	return color.NRGBA{R: brand.R, G: brand.G, B: brand.B, A: alpha}
}

// interactionAlpha is how strongly each pointer state lifts a control off its
// background. They are faint on purpose: focus and hover fire constantly, and
// anything heavier reads as a selection rather than as a pointer passing over.
func interactionAlpha(name fyne.ThemeColorName) uint8 {
	switch name {
	case theme.ColorNamePressed:
		return 0x24
	case theme.ColorNameFocus:
		return 0x14
	default:
		return 0x0E
	}
}

// palette is the set of colours one variant needs. Two of them exist because
// the window follows the desktop's light or dark setting rather than picking
// for the user, and a palette that only works in one is a window that is
// unreadable half the time.
type palette struct {
	background color.Color
	// surface is what sits on top of the background: the stat tiles, an
	// inactive button, a menu.
	surface    color.Color
	foreground color.Color
	// muted is for text that labels something rather than being the something.
	muted     color.Color
	separator color.Color
	success   color.Color
	warning   color.Color
	failure   color.Color
}

// The two palettes are deliberately low-contrast in their greys and saturated
// only where meaning lives: a status word, the primary action. Everything
// competing for attention is how the default look ends up feeling busy.
var (
	darkPalette = palette{
		background: color.NRGBA{R: 0x16, G: 0x18, B: 0x1D, A: 0xFF},
		surface:    color.NRGBA{R: 0x1F, G: 0x23, B: 0x2B, A: 0xFF},
		foreground: color.NRGBA{R: 0xE6, G: 0xE8, B: 0xEB, A: 0xFF},
		muted:      color.NRGBA{R: 0x8B, G: 0x93, B: 0x9F, A: 0xFF},
		separator:  color.NRGBA{R: 0x2A, G: 0x2F, B: 0x38, A: 0xFF},
		success:    color.NRGBA{R: 0x3F, G: 0xB9, B: 0x50, A: 0xFF},
		warning:    color.NRGBA{R: 0xD2, G: 0x99, B: 0x22, A: 0xFF},
		failure:    color.NRGBA{R: 0xF8, G: 0x51, B: 0x49, A: 0xFF},
	}

	lightPalette = palette{
		background: color.NRGBA{R: 0xFB, G: 0xFB, B: 0xFC, A: 0xFF},
		surface:    color.NRGBA{R: 0xF0, G: 0xF1, B: 0xF3, A: 0xFF},
		foreground: color.NRGBA{R: 0x1A, G: 0x1D, B: 0x21, A: 0xFF},
		muted:      color.NRGBA{R: 0x63, G: 0x6B, B: 0x77, A: 0xFF},
		separator:  color.NRGBA{R: 0xE0, G: 0xE2, B: 0xE6, A: 0xFF},
		success:    color.NRGBA{R: 0x1A, G: 0x7F, B: 0x37, A: 0xFF},
		warning:    color.NRGBA{R: 0x9A, G: 0x6A, B: 0x00, A: 0xFF},
		failure:    color.NRGBA{R: 0xCF, G: 0x22, B: 0x2E, A: 0xFF},
	}
)

// paletteFor picks the palette for a variant. Anything that is not explicitly
// light is treated as dark, which is the safer default: light text on a dark
// ground stays legible on a light one far better than the reverse.
func paletteFor(variant fyne.ThemeVariant) palette {
	if variant == theme.VariantLight {
		return lightPalette
	}

	return darkPalette
}

// Theme is the application's look: the default Fyne theme with this project's
// colours and a slightly larger type and spacing scale.
//
// It wraps rather than replaces the default so that fonts and icons keep
// coming from Fyne — those are the parts a hand-rolled theme gets wrong, and
// the parts nobody notices until a glyph is missing.
func Theme() fyne.Theme { return brandTheme{base: theme.DefaultTheme()} }

type brandTheme struct{ base fyne.Theme }

func (t brandTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	colors := paletteFor(variant)

	switch name {
	case theme.ColorNamePrimary, theme.ColorNameHyperlink:
		return brand
	case theme.ColorNameFocus, theme.ColorNamePressed, theme.ColorNameHover:
		// Neutral, not brand. buttonColorNames blends these into a control's
		// whole background, so an orange focus paints the focused Details row
		// as a brown slab — and the brand colour stops meaning "this is the
		// action" the moment three other things are wearing it.
		return withAlpha(colors.foreground, interactionAlpha(name))
	case theme.ColorNameSelection:
		// Selected text is the one place the accent reads as emphasis rather
		// than as a second button.
		return tint(0x33)
	case theme.ColorNameBackground:
		return colors.background
	case theme.ColorNameButton, theme.ColorNameInputBackground,
		theme.ColorNameMenuBackground, theme.ColorNameOverlayBackground,
		theme.ColorNameHeaderBackground:
		return colors.surface
	case theme.ColorNameForeground:
		return colors.foreground
	case theme.ColorNameDisabled, theme.ColorNamePlaceHolder:
		return colors.muted
	case theme.ColorNameSeparator, theme.ColorNameInputBorder:
		return colors.separator
	case theme.ColorNameSuccess:
		return colors.success
	case theme.ColorNameWarning:
		return colors.warning
	case theme.ColorNameError:
		return colors.failure
	case theme.ColorNameForegroundOnPrimary, theme.ColorNameForegroundOnError,
		theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning:
		// White on every filled control. The brand orange and the state
		// colours are all mid-tone, so a foreground that followed the variant
		// would be dark text on orange in light mode — which is exactly the
		// combination that fails a contrast check.
		return color.NRGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	}

	return t.base.Color(name, variant)
}

func (t brandTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameHeadingText:
		return 22
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNameButtonRadius:
		return 10
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return 8
	case theme.SizeNameCardRadius:
		return 12
	case theme.SizeNameScrollBar:
		return 8
	}

	return t.base.Size(name)
}

func (t brandTheme) Font(style fyne.TextStyle) fyne.Resource { return t.base.Font(style) }

func (t brandTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.base.Icon(name) }
