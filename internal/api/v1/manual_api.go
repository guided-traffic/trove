package v1

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/manual"
)

// The manual operations of R-API-054: the contents and the pages of the manual the
// binary embeds (internal/manual), rendered once at start-up. Every caller reads the
// same text, so the answers carry the manual's ETag and no view.

// GetManualContents implements GET /api/v1/manual.
func (s *Server) GetManualContents(w http.ResponseWriter, r *http.Request) {
	m, err := s.manual(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	if NotModified(w, r, m.ETag()) {
		return
	}
	out := gen.ManualContents{Sections: []gen.ManualSection{}}
	for _, sec := range m.Sections() {
		gs := gen.ManualSection{Id: sec.ID, Title: sec.Title, Pages: []gen.ManualPageSummary{}}
		for _, p := range sec.Pages {
			gs.Pages = append(gs.Pages, gen.ManualPageSummary{Id: p.ID, Title: p.Title, Summary: p.Summary})
		}
		out.Sections = append(out.Sections, gs)
	}
	w.Header().Set("ETag", m.ETag())
	writeJSON(w, r, http.StatusOK, out)
}

// GetManualPage implements GET /api/v1/manual/{section}/{page}.
func (s *Server) GetManualPage(w http.ResponseWriter, r *http.Request, section, page string) {
	m, err := s.manual(r)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	p, ok := m.Page(section, page)
	if !ok {
		httpserver.Problem(w, r, errs.E(errs.NotFound, errs.CodeNotFound, "The manual has no such page; GET /api/v1/manual lists the pages."))
		return
	}
	if NotModified(w, r, m.ETag()) {
		return
	}
	out := gen.ManualPage{
		Section: p.Section, Id: p.ID, Title: p.Title, Summary: p.Summary, Html: p.HTML,
		Headings: []gen.ManualHeading{}, Previous: manualRef(p.Previous), Next: manualRef(p.Next),
	}
	for _, h := range p.Headings {
		out.Headings = append(out.Headings, gen.ManualHeading{Level: gen.ManualHeadingLevel(h.Level), Id: h.ID, Text: h.Text})
	}
	w.Header().Set("ETag", m.ETag())
	writeJSON(w, r, http.StatusOK, out)
}

// manual returns the loaded manual after the gate of a public operation: an
// anonymous caller reads it only where anonymous access is on.
func (s *Server) manual(r *http.Request) (*manual.Manual, error) {
	if err := s.anonymousGate(r.Context()); err != nil {
		return nil, err
	}
	if s.o.Manual != nil {
		return s.o.Manual, nil
	}
	if s.o.LoadManual != nil {
		if m, err := s.o.LoadManual(); err == nil && m != nil {
			return m, nil
		}
	}
	return nil, unavailable("The manual")
}

func manualRef(p *manual.Page) *gen.ManualPageRef {
	if p == nil {
		return nil
	}
	return &gen.ManualPageRef{Section: p.Section, Id: p.ID, Title: p.Title}
}
