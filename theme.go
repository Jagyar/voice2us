package main

// Тема Voice2Us: спокойная нейтральная палитра с одним акцентом, мягкие
// скругления, крупнее отступы. Светлая и тёмная версии выбираются по
// системной теме (macOS и Windows).

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Собственные имена цветов (кроме стандартных из пакета theme).
const (
	colorCard   fyne.ThemeColorName = "v2uCard"   // фон карточки
	colorBorder fyne.ThemeColorName = "v2uBorder" // тонкая рамка карточки
	colorSoft   fyne.ThemeColorName = "v2uSoft"   // приглушённая заливка акцента (аватар)
)

type palette map[fyne.ThemeColorName]color.Color

func rgb(hex uint32) color.Color {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 255}
}

func rgba(hex uint32, a uint8) color.Color {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: a}
}

var darkPalette = palette{
	theme.ColorNameBackground:          rgb(0x0E1014),
	theme.ColorNameOverlayBackground:   rgb(0x151821),
	theme.ColorNameMenuBackground:      rgb(0x151821),
	theme.ColorNameHeaderBackground:    rgb(0x151821),
	theme.ColorNameForeground:          rgb(0xE8EAF0),
	theme.ColorNamePlaceHolder:         rgb(0x8A92A6),
	theme.ColorNameDisabled:            rgb(0x5B6275),
	theme.ColorNameDisabledButton:      rgb(0x1A1E28),
	theme.ColorNameButton:              rgb(0x1E2330),
	theme.ColorNameInputBackground:     rgb(0x161A23),
	theme.ColorNameInputBorder:         rgb(0x2A3040),
	theme.ColorNameSeparator:           rgb(0x222735),
	theme.ColorNamePrimary:             rgb(0x7C8CFF),
	theme.ColorNameForegroundOnPrimary: rgb(0x0B0D14),
	theme.ColorNameFocus:               rgba(0x7C8CFF, 0x55),
	theme.ColorNameSelection:           rgba(0x7C8CFF, 0x40),
	theme.ColorNameHover:               rgba(0xFFFFFF, 0x0F),
	theme.ColorNamePressed:             rgba(0xFFFFFF, 0x1A),
	theme.ColorNameHyperlink:           rgb(0x7C8CFF),
	theme.ColorNameSuccess:             rgb(0x3DDC97),
	theme.ColorNameWarning:             rgb(0xF5B14C),
	theme.ColorNameError:               rgb(0xFF6B7A),
	theme.ColorNameForegroundOnError:   rgb(0x14070A),
	theme.ColorNameForegroundOnSuccess: rgb(0x04140D),
	theme.ColorNameForegroundOnWarning: rgb(0x1A1004),
	theme.ColorNameScrollBar:           rgba(0xFFFFFF, 0x30),
	theme.ColorNameShadow:              rgba(0x000000, 0x66),
	colorCard:                          rgb(0x151821),
	colorBorder:                        rgb(0x232838),
	colorSoft:                          rgba(0x7C8CFF, 0x2E),
}

var lightPalette = palette{
	theme.ColorNameBackground:          rgb(0xF5F6F9),
	theme.ColorNameOverlayBackground:   rgb(0xFFFFFF),
	theme.ColorNameMenuBackground:      rgb(0xFFFFFF),
	theme.ColorNameHeaderBackground:    rgb(0xFFFFFF),
	theme.ColorNameForeground:          rgb(0x151822),
	theme.ColorNamePlaceHolder:         rgb(0x6B7385),
	theme.ColorNameDisabled:            rgb(0xA3A9B8),
	theme.ColorNameDisabledButton:      rgb(0xECEEF3),
	theme.ColorNameButton:              rgb(0xE9ECF3),
	theme.ColorNameInputBackground:     rgb(0xFFFFFF),
	theme.ColorNameInputBorder:         rgb(0xD5D9E3),
	theme.ColorNameSeparator:           rgb(0xE3E6EE),
	theme.ColorNamePrimary:             rgb(0x4F5BD5),
	theme.ColorNameForegroundOnPrimary: rgb(0xFFFFFF),
	theme.ColorNameFocus:               rgba(0x4F5BD5, 0x55),
	theme.ColorNameSelection:           rgba(0x4F5BD5, 0x30),
	theme.ColorNameHover:               rgba(0x000000, 0x0A),
	theme.ColorNamePressed:             rgba(0x000000, 0x14),
	theme.ColorNameHyperlink:           rgb(0x4F5BD5),
	theme.ColorNameSuccess:             rgb(0x16A765),
	theme.ColorNameWarning:             rgb(0xD98A12),
	theme.ColorNameError:               rgb(0xD93B4F),
	theme.ColorNameForegroundOnError:   rgb(0xFFFFFF),
	theme.ColorNameForegroundOnSuccess: rgb(0xFFFFFF),
	theme.ColorNameForegroundOnWarning: rgb(0xFFFFFF),
	theme.ColorNameScrollBar:           rgba(0x000000, 0x30),
	theme.ColorNameShadow:              rgba(0x1A2033, 0x22),
	colorCard:                          rgb(0xFFFFFF),
	colorBorder:                        rgb(0xE3E6EE),
	colorSoft:                          rgba(0x4F5BD5, 0x1F),
}

type appTheme struct{ fyne.Theme }

func newAppTheme() fyne.Theme { return &appTheme{Theme: theme.DefaultTheme()} }

func (t *appTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	p := darkPalette
	if variant == theme.VariantLight {
		p = lightPalette
	}
	if c, ok := p[name]; ok {
		return c
	}
	return t.Theme.Color(name, variant)
}

func (t *appTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 6
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameText:
		return 14
	case theme.SizeNameHeadingText:
		return 26
	case theme.SizeNameSubHeadingText:
		return 17
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameInputRadius, theme.SizeNameButtonRadius, theme.SizeNameSelectionRadius:
		return 10
	case theme.SizeNameCardRadius, theme.SizeNameDialogRadius, theme.SizeNamePopupRadius, theme.SizeNameMenuRadius:
		return 14
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameScrollBar, theme.SizeNameScrollBarSmall:
		return 6
	}
	return t.Theme.Size(name)
}
