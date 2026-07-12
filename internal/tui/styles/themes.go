package styles

import "charm.land/lipgloss/v2"

// Theme pairs a stable name with a Palette. Names are persisted in user
// preferences, so treat them as API: add new ones, don't rename.
type Theme struct {
	Name    string
	Palette Palette
}

// Themes returns the theme registry in cycle order.
func Themes() []Theme {
	return []Theme{
		{Name: "windshift-dark", Palette: WindshiftDark()},
		{Name: "void", Palette: Void()},
		{Name: "onyx", Palette: Onyx()},
		{Name: "system", Palette: System()},
	}
}

// DefaultTheme is the fallback for unknown/unset preferences.
const DefaultTheme = "windshift-dark"

// ByName resolves a theme by name, falling back to the default.
func ByName(name string) Theme {
	for _, t := range Themes() {
		if t.Name == name {
			return t
		}
	}
	return Themes()[0]
}

// Next returns the theme after current in cycle order (wrapping).
func Next(current string) Theme {
	ts := Themes()
	for i, t := range ts {
		if t.Name == current {
			return ts[(i+1)%len(ts)]
		}
	}
	return ts[0]
}

// Void is the OLED-black variant: true-black base, near-black surfaces,
// brand accents kept.
func Void() Palette {
	return Palette{
		Primary:          hex("#2874bb"),
		PrimaryHovered:   hex("#3b82f6"),
		PrimarySubtle:    hex("#09326c"),
		Accent:           hex("#8b5cf6"),
		FgBase:           hex("#c9d1d9"),
		FgSubtle:         hex("#8c9bab"),
		FgMuted:          hex("#5e6c84"),
		FgInverse:        hex("#000000"),
		BgBase:           hex("#000000"),
		BgSurface:        hex("#0a0a0a"),
		BgSurfaceHovered: hex("#161616"),
		BgOverlay:        hex("#0a0a0a"),
		Border:           hex("#262626"),
		BorderSubtle:     hex("#1a1a1a"),
		BorderFocus:      hex("#579dff"),
		Success:          hex("#2874bb"),
		Warning:          hex("#ca8a04"),
		Danger:           hex("#dc2626"),
		Info:             hex("#579dff"),
		Selected:         hex("#09326c"),
		OnPrimary:        hex("#ffffff"),
		GradFrom:         hex("#3b82f6"),
		GradTo:           hex("#8b5cf6"),
	}
}

// Onyx is the monochrome variant: grayscale chrome, near-white primary.
// Status/priority chips keep their API colors — only the chrome
// desaturates.
func Onyx() Palette {
	return Palette{
		Primary:          hex("#e6e6e6"),
		PrimaryHovered:   hex("#ffffff"),
		PrimarySubtle:    hex("#333333"),
		Accent:           hex("#bfbfbf"),
		FgBase:           hex("#d4d4d4"),
		FgSubtle:         hex("#8a8a8a"),
		FgMuted:          hex("#5c5c5c"),
		FgInverse:        hex("#111111"),
		BgBase:           hex("#111111"),
		BgSurface:        hex("#1c1c1c"),
		BgSurfaceHovered: hex("#262626"),
		BgOverlay:        hex("#181818"),
		Border:           hex("#333333"),
		BorderSubtle:     hex("#242424"),
		BorderFocus:      hex("#e6e6e6"),
		Success:          hex("#bfbfbf"),
		Warning:          hex("#8a8a8a"),
		Danger:           hex("#e6e6e6"),
		Info:             hex("#bfbfbf"),
		Selected:         hex("#333333"),
		OnPrimary:        hex("#111111"),
		GradFrom:         hex("#f5f5f5"),
		GradTo:           hex("#8a8a8a"),
	}
}

// System is built entirely from the 16 ANSI palette slots so the user's own
// terminal theme shows through — and it is downsample-proof over SSH
// connections that only advertise 16 colors.
func System() Palette {
	ansi := lipgloss.Color
	return Palette{
		Primary:          ansi("4"),  // blue
		PrimaryHovered:   ansi("12"), // bright blue
		PrimarySubtle:    ansi("4"),
		Accent:           ansi("5"), // magenta
		FgBase:           ansi("7"), // white
		FgSubtle:         ansi("8"), // bright black
		FgMuted:          ansi("8"),
		FgInverse:        ansi("0"),
		BgBase:           ansi("0"), // black
		BgSurface:        ansi("0"),
		BgSurfaceHovered: ansi("8"),
		BgOverlay:        ansi("0"),
		Border:           ansi("8"),
		BorderSubtle:     ansi("8"),
		BorderFocus:      ansi("12"),
		Success:          ansi("2"), // green
		Warning:          ansi("3"), // yellow
		Danger:           ansi("1"), // red
		Info:             ansi("6"), // cyan
		Selected:         ansi("4"),
		OnPrimary:        ansi("15"), // bright white
		GradFrom:         ansi("12"),
		GradTo:           ansi("13"),
	}
}
