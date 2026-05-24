// Package styles holds the visual theme for the Windshift TUI.
//
// The palette mirrors the web design system's semantic tokens
// (frontend/src/lib/design-system/tokens/semantic-dark.css) so the SSH TUI
// reads as the same product as the web UI. Add a light palette here when a
// terminal that hints at a light background warrants it.
package styles

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Palette holds raw colors keyed by semantic role.
type Palette struct {
	Primary          color.Color
	PrimaryHovered   color.Color
	PrimarySubtle    color.Color
	Accent           color.Color
	FgBase           color.Color
	FgSubtle         color.Color
	FgMuted          color.Color
	FgInverse        color.Color
	BgBase           color.Color
	BgSurface        color.Color
	BgSurfaceHovered color.Color
	BgOverlay        color.Color
	Border           color.Color
	BorderSubtle     color.Color
	BorderFocus      color.Color
	Success          color.Color
	Warning          color.Color
	Danger           color.Color
	Info             color.Color
	Selected         color.Color
	OnPrimary        color.Color
	GradFrom         color.Color
	GradTo           color.Color
}

func hex(s string) color.Color { return lipgloss.Color(s) }
