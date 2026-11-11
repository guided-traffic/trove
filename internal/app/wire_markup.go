package app

import (
	"fmt"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/packages/readme"
)

// readmeMarkup is the Host's Markup (ADR-196): the README renderer of the installation, with the operator's image origins, so a format's
// documentation is rendered and sanitized under the one policy of R-UI-008.
type readmeMarkup struct{ r *readme.Renderer }

var _ formats.Markup = readmeMarkup{}

// Markdown implements [formats.Markup].
func (m readmeMarkup) Markdown(src string) (string, bool) {
	res := m.r.Render(src)
	return res.HTML, res.Truncated
}

// SanitizeHTML implements [formats.Markup].
func (m readmeMarkup) SanitizeHTML(html string) string { return m.r.SanitizeHTML(html) }

// hostMarkup builds the Markup of a Host from the ui.readme keys, as the README operation reads them. A configuration the renderer
// refuses stops the start-up: the same keys stop wirePackagesAPI.
func (a *App) hostMarkup() (formats.Markup, error) {
	rd, err := readme.New(readme.Options{
		MaxSourceBytes: int(a.cfg.UI.Readme.MaxSourceBytes),
		ImageOrigins:   a.cfg.UI.Readme.ExternalImageOrigins,
		OwnOrigins:     a.ownOrigins(),
	})
	if err != nil {
		return nil, fmt.Errorf("the documentation renderer of the format host: %w", err)
	}
	return readmeMarkup{r: rd}, nil
}
