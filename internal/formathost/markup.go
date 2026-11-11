package formathost

import "github.com/guided-traffic/trove/internal/formats"

// Markup implements [formats.Host]: the renderer the composition root wired (Options.Markup), or [formats.NoMarkup] for a replica that
// has not wired it.
func (h *Host) Markup() formats.Markup {
	if h.o.Markup == nil {
		return formats.NoMarkup()
	}
	return h.o.Markup
}
