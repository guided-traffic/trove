package formathost

import "testing"

type markupProbe struct{}

func (markupProbe) Markdown(string) (string, bool) { return "wired", false }
func (markupProbe) SanitizeHTML(string) string     { return "wired" }

// TestR_ANS_039_TheHostAnswersTheWiredMarkupOrNoMarkup: Host.Markup is the renderer the composition root gave the options, and a host
// without one answers formats.NoMarkup, never nil.
func TestR_ANS_039_TheHostAnswersTheWiredMarkupOrNoMarkup(t *testing.T) {
	bare := &Host{}
	if m := bare.Markup(); m == nil || m.SanitizeHTML("<p>x</p>") != "" {
		t.Errorf("a host without a renderer answers %v", m)
	}
	wired := &Host{o: Options{Markup: markupProbe{}}}
	if got := wired.Markup().SanitizeHTML("x"); got != "wired" {
		t.Errorf("a host with a renderer answers %q", got)
	}
}
