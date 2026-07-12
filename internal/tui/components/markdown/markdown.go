// Package markdown renders Markdown to ANSI for the detail pane via
// glamour, themed from the active palette. Renderers are width-bound —
// create one per pane width and recreate on resize/theme change.
package markdown

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	gstyles "charm.land/glamour/v2/styles"

	"windshift/internal/tui/styles"
)

// Renderer wraps a width-bound glamour TermRenderer. A nil Renderer (or one
// whose construction failed) degrades to plain text — markdown rendering is
// never load-bearing.
type Renderer struct {
	tr    *glamour.TermRenderer
	width int
}

// New builds a renderer for the given pane width. Never use glamour's
// AutoStyle here — there is no local terminal to probe over SSH; the style
// derives from the palette instead.
func New(s *styles.Styles, width int) *Renderer {
	if width < 10 {
		width = 10
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStyles(styleConfig(s)),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return &Renderer{width: width}
	}
	return &Renderer{tr: tr, width: width}
}

// Render converts markdown to ANSI. Input must already be sanitized; the
// fallback path returns it unchanged.
func (r *Renderer) Render(md string) string {
	if r == nil || r.tr == nil {
		return md
	}
	out, err := r.tr.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}

// styleConfig derives a glamour style from the palette: glamour's dark
// config as the base, with document/heading/link/quote colors re-anchored
// to the theme and the pane-hostile document margin removed.
func styleConfig(s *styles.Styles) ansi.StyleConfig {
	cfg := gstyles.DarkStyleConfig

	zero := uint(0)
	cfg.Document.Margin = &zero
	cfg.Document.Color = hexPtr(s.Palette.FgBase)
	cfg.Paragraph.Color = hexPtr(s.Palette.FgBase)

	heading := hexPtr(s.Palette.PrimaryHovered)
	cfg.Heading.Color = heading
	cfg.H1.Color = hexPtr(s.Palette.OnPrimary)
	cfg.H1.BackgroundColor = hexPtr(s.Palette.Primary)
	cfg.H2.Color = heading
	cfg.H3.Color = heading

	cfg.Link.Color = hexPtr(s.Palette.Info)
	cfg.LinkText.Color = hexPtr(s.Palette.Info)
	cfg.BlockQuote.Color = hexPtr(s.Palette.FgMuted)
	cfg.HorizontalRule.Color = hexPtr(s.Palette.BorderSubtle)

	return cfg
}

func hexPtr(c color.Color) *string {
	if c == nil {
		return nil
	}
	r, g, b, _ := c.RGBA()
	h := fmt.Sprintf("#%02x%02x%02x", uint8(r>>8&0xff), uint8(g>>8&0xff), uint8(b>>8&0xff)) //nolint:gosec // masked to 8 bits

	return &h
}
