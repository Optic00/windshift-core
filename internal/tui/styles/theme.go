package styles

// WindshiftDark returns the dark palette, lifted from the web design system's
// dark theme tokens. Primary #2874bb is the brand anchor; surface/border tones
// match the chromeless dark chrome the web UI uses.
func WindshiftDark() Palette {
	return Palette{
		Primary:          hex("#2874bb"),
		PrimaryHovered:   hex("#3b82f6"),
		PrimarySubtle:    hex("#09326c"),
		Accent:           hex("#8b5cf6"),
		FgBase:           hex("#b6c2cf"),
		FgSubtle:         hex("#8c9bab"),
		FgMuted:          hex("#5e6c84"),
		FgInverse:        hex("#1d2125"),
		BgBase:           hex("#1d2125"),
		BgSurface:        hex("#282e33"),
		BgSurfaceHovered: hex("#313940"),
		BgOverlay:        hex("#22272b"),
		Border:           hex("#3a424a"),
		BorderSubtle:     hex("#2c333a"),
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
