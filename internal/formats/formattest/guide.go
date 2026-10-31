package formattest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideSnippetSections are the setup sections a guide section may attach
// (formats.GuideSection.Snippets).
func guideSnippetSections() []string { return []string{"configure", "install", "publish"} }

var tokenShape = regexp.MustCompile(`trv_(pat|sat|cit)_`)

// guide is the guide half of check 8 (R-UI-049): a format with clients ships a
// guide that covers every class and client with the required topics, attaches the
// setup snippets to its configure and install topics, and obeys the credential
// rule of the templates in the code a reader copies.
func (r *run) guide(tpls []formats.SetupTemplate) {
	secs := r.f.Guide()
	if len(r.d.Clients) == 0 {
		if len(secs) > 0 {
			r.failf("the format has a guide and no clients")
		}
		return
	}
	tabs := map[string]map[string]bool{}
	for _, t := range tpls {
		if tabs[t.Client] == nil {
			tabs[t.Client] = map[string]bool{}
		}
		tabs[t.Client][t.Section] = true
	}
	ids := map[string]bool{}
	for _, s := range secs {
		if ids[s.ID] {
			r.failf("guide section ID %q is used twice", s.ID)
		}
		ids[s.ID] = true
		r.guideSection(s, tabs)
	}
	for _, class := range r.d.Classes {
		for _, c := range r.d.Clients {
			r.guideCoverage(secs, class, c.ID)
		}
	}
}

// guideSection checks one section's declaration and runs it over every variant of
// the data for each class and client it applies to.
func (r *run) guideSection(s formats.GuideSection, tabs map[string]map[string]bool) {
	switch {
	case s.ID == "":
		r.failf("a guide section of topic %q has no ID", s.Topic)
		return
	case !slices.Contains(formats.GuideTopics(), s.Topic):
		r.failf("guide section %q has topic %q", s.ID, s.Topic)
	case strings.TrimSpace(s.Title) == "":
		r.failf("guide section %q has no title", s.ID)
	case s.Snippets != "" && !slices.Contains(guideSnippetSections(), s.Snippets):
		r.failf("guide section %q attaches the snippets of section %q", s.ID, s.Snippets)
	}
	for _, c := range s.Classes {
		if !r.d.Supports(c) {
			r.failf("guide section %q is written for class %q, which the format does not support", s.ID, c)
		}
	}
	for _, id := range s.Clients {
		if !slices.ContainsFunc(r.d.Clients, func(c formats.Client) bool { return c.ID == id }) {
			r.failf("guide section %q is written for client %q, which the descriptor does not list", s.ID, id)
		}
	}
	for _, c := range r.d.Clients {
		if s.Snippets != "" && !tabs[c.ID][s.Snippets] && slices.ContainsFunc(r.d.Classes, func(class formats.Class) bool { return s.AppliesTo(class, c.ID) }) {
			r.failf("guide section %q attaches the %s snippets of client %q, which has none", s.ID, s.Snippets, c.ID)
		}
		for _, class := range r.d.Classes {
			if !s.AppliesTo(class, c.ID) {
				continue
			}
			for _, d := range r.guideVariants(class, c.ID) {
				if !r.guideText(s, d) {
					return
				}
			}
		}
	}
}

// guideText executes a section over one variant and applies the credential rule.
// It reports false after the first failure, so that one defect is named once and
// not once per variant.
func (r *run) guideText(s formats.GuideSection, d formats.GuideData) bool {
	out, err := formats.ExecuteGuide(s, d)
	switch {
	case err != nil:
		r.failf("guide section %q does not execute over GuideData: %v", s.ID, err)
		return false
	case strings.Contains(out, "<no value>"):
		r.failf("guide section %q renders %q for class %s, client %s", s.ID, "<no value>", d.Class, d.Client)
		return false
	case tokenShape.MatchString(out):
		r.failf("guide section %q holds the shape of a token", s.ID)
		return false
	}
	for _, code := range fencedCode(out) {
		for _, o := range append([]string{formats.SetupTokenRef, d.TokenEnvVar}, r.fx.OptionNames...) {
			code = strings.ReplaceAll(code, o, "")
		}
		if m := tokenText.FindString(code); m != "" {
			r.failf("guide section %q spells a credential in a code block (%q): a guide never receives a token, the user supplies it", s.ID, m)
			return false
		}
	}
	return true
}

// guideCoverage: every required topic has a section for the class and the client
// that says something in every variant; the configure and install topics attach the
// setup snippets of their section.
func (r *run) guideCoverage(secs []formats.GuideSection, class formats.Class, client string) {
	for _, topic := range formats.RequiredGuideTopics() {
		var found, attached bool
		for _, s := range secs {
			if s.Topic != topic || !s.AppliesTo(class, client) {
				continue
			}
			found = true
			attached = attached || s.Snippets == topic
			for _, d := range r.guideVariants(class, client) {
				if out, err := formats.ExecuteGuide(s, d); err == nil && strings.TrimSpace(out) == "" && s.Snippets == "" {
					r.failf("guide section %q of the required topic %q says nothing for class %s, client %s (%s)", s.ID, topic, class, client, variantName(d))
				}
			}
		}
		switch {
		case !found:
			r.failf("class %s, client %q: no guide section of topic %q", class, client, topic)
		case (topic == formats.GuideConfigure || topic == formats.GuideInstall) && !attached:
			r.failf("class %s, client %q: no guide section of topic %q attaches the %s snippets", class, client, topic, topic)
		}
	}
}

// guideVariants is the data a section is run over for a class and a client: a named
// repository and the format's guide, a reader who may publish and one who may not,
// signed in and anonymous, on an endpoint with and without exposure rules.
func (r *run) guideVariants(class formats.Class, client string) []formats.GuideData {
	var out []formats.GuideData
	for _, key := range []string{"main", ""} {
		registry := DefaultBaseURL + "/api/" + string(r.d.ID) + "/" + key + "/"
		if key == "" {
			registry = DefaultBaseURL + "/api/" + string(r.d.ID) + "/" + formats.GuideRepositoryPlaceholder + "/"
		}
		targets := []string{""}
		if key != "" && class != formats.Remote {
			targets = append(targets, key)
		}
		for _, target := range targets {
			for _, anonymous := range []bool{false, true} {
				for _, rules := range []bool{false, true} {
					out = append(out, formats.GuideData{
						SetupData: formats.SetupData{
							RegistryURL: registry, Repository: key, Endpoint: "default",
							RegistryKey: strings.TrimPrefix(registry, "https:"), RequiresAuth: !anonymous,
						},
						Class: class, Client: client, PublishTarget: target, Anonymous: anonymous,
						ExposureRules: rules, TokenEnvVar: "TROVE_TOKEN",
					})
				}
			}
		}
	}
	return out
}

func variantName(d formats.GuideData) string {
	return fmt.Sprintf("repository %q, publish target %q, anonymous %v, exposure rules %v", d.Repository, d.PublishTarget, d.Anonymous, d.ExposureRules)
}

var fence = regexp.MustCompile("^\\s*(```|~~~)")

// fencedCode returns the fenced code blocks of a Markdown text: what a reader
// copies into a file or a shell.
func fencedCode(md string) []string {
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		if fence.MatchString(line) {
			if in {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			in = !in
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	if in {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}
