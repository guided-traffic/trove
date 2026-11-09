package deb

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is the guide of the Debian format (R-DEB-029, api-and-ui.md R-UI-049): what a local repository is, how an APT client trusts its
// key and adds it as a source, which credential it sends, how it installs, how a package is uploaded and removed, and what each answer
// and each message of the client means. The sections of the remote and virtual class are written with those classes.
func (f *Format) Guide() []formats.GuideSection {
	local, writable := []formats.Class{formats.Local}, []formats.Class{formats.Local, formats.Virtual}
	secs := []formats.GuideSection{
		{ID: "deb.local.what", Topic: formats.GuideWhat, Title: "What a local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "deb.configure", Topic: formats.GuideConfigure, Title: "Add the repository as a source", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "deb.authenticate", Topic: formats.GuideAuthenticate, Title: "Which credential APT sends", Markdown: guideText("authenticate")},
		{ID: "deb.install", Topic: formats.GuideInstall, Title: "Install a package", Snippets: "install", Markdown: guideText("install")},
		{ID: "deb.publish", Topic: formats.GuidePublish, Title: "Upload a package", Classes: writable, Snippets: "publish", Markdown: guideText("publish")},
		{ID: "deb.maintain", Topic: formats.GuideMaintain, Title: "Move, delete and re-sign", Classes: local, Markdown: guideText("maintain")},
		{ID: "deb.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
	}
	secs = append(secs, remoteGuideSections()...)
	secs = append(secs, virtualGuideSections()...)
	return supportedOnly(f.Descriptor(), secs)
}

// supportedOnly keeps, of each section, the classes the format registers: the kit refuses a section written for a class the
// descriptor does not list (check 8), and the remote and the virtual class join with their adapters.
func supportedOnly(d formats.Descriptor, secs []formats.GuideSection) []formats.GuideSection {
	var out []formats.GuideSection
	for _, s := range secs {
		if len(s.Classes) > 0 {
			var kept []formats.Class
			for _, c := range s.Classes {
				if d.Supports(c) {
					kept = append(kept, c)
				}
			}
			if len(kept) == 0 {
				continue
			}
			s.Classes = kept
		}
		out = append(out, s)
	}
	return out
}

// guideText is the template of one section; a missing file is a defect of the build, which the conformance kit reports as a required
// topic that says nothing.
func guideText(name string) string {
	b, err := guideFiles.ReadFile("guide/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(b)
}
