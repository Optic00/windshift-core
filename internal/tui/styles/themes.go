package styles

// Theme pairs a stable preference name with a terminalcolors.com palette.
type Theme struct {
	Name        string
	Label       string
	Description string
	Palette     Palette
}

func Themes() []Theme {
	return []Theme{
		{Name: "catppuccin-mocha", Label: "Catppuccin", Description: "Mocha", Palette: CatppuccinMocha()},
		{Name: "catppuccin-frappe", Label: "Catppuccin", Description: "Frappé", Palette: CatppuccinFrappe()},
		{Name: "dracula", Label: "Dracula", Description: "Default", Palette: Dracula()},
		{Name: "nord", Label: "Nord", Description: "Default", Palette: Nord()},
		{Name: "gruvbox-dark", Label: "Gruvbox", Description: "Dark", Palette: GruvboxDark()},
		{Name: "tokyo-night", Label: "Tokyo Night", Description: "Default", Palette: TokyoNight()},
		{Name: "kanagawa-wave", Label: "Kanagawa", Description: "Wave", Palette: KanagawaWave()},
		{Name: "rose-pine", Label: "Rosé Pine", Description: "Default", Palette: RosePine()},
		{Name: "everforest-dark", Label: "Everforest", Description: "Dark", Palette: EverforestDark()},
		{Name: "solarized-dark", Label: "Solarized", Description: "Dark", Palette: SolarizedDark()},
		{Name: "one-dark", Label: "One", Description: "Dark", Palette: OneDark()},
	}
}

const DefaultTheme = "catppuccin-mocha"

func ByName(name string) Theme {
	// Preferences written by the earlier custom theme registry migrate to the
	// new default instead of becoming permanently stuck on a removed palette.
	switch name {
	case "windshift-dark", "void", "onyx", "system":
		name = DefaultTheme
	}
	for _, theme := range Themes() {
		if theme.Name == name {
			return theme
		}
	}
	return Themes()[0]
}

func Next(current string) Theme {
	themes := Themes()
	for i, theme := range themes {
		if theme.Name == current {
			return themes[(i+1)%len(themes)]
		}
	}
	return themes[0]
}
