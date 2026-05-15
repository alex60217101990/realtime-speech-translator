// Package ui hosts shared Fyne helpers used by cmd/translator.
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// ApplyTheme switches the running Fyne app between light / dark /
// system themes. Unknown names fall back to system. The function is a
// no-op when the app is nil.
func ApplyTheme(a fyne.App, name string) {
	if a == nil {
		return
	}
	switch name {
	case "light":
		a.Settings().SetTheme(theme.LightTheme())
	case "dark":
		a.Settings().SetTheme(theme.DarkTheme())
	default:
		a.Settings().SetTheme(theme.DefaultTheme())
	}
}
