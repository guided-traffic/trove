package generic

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is the generic format's guide (R-GENERIC-016, api-and-ui.md R-UI-049): what
// a generic local repository is, and how curl signs in, downloads, checks and
// resumes downloads, lists folders, uploads with a digest or only once, replaces
// and deletes files through /files/, what an endpoint with exposure rules changes,
// and what each answer of /files/ means. Every statement describes trove as built;
// the tests in guide_test.go pin the facts the text names to the code.
func (Format) Guide() []formats.GuideSection {
	local := []formats.Class{formats.Local}
	return []formats.GuideSection{
		{ID: "generic.local.what", Topic: formats.GuideWhat, Title: "What a generic local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "generic.configure", Topic: formats.GuideConfigure, Title: "Point curl at the repository", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "generic.authenticate", Topic: formats.GuideAuthenticate, Title: "Sign curl in", Markdown: guideText("authenticate")},
		{ID: "generic.install", Topic: formats.GuideInstall, Title: "Download files", Snippets: "install", Markdown: guideText("install")},
		{ID: "generic.publish", Topic: formats.GuidePublish, Title: "Upload files", Classes: local, Snippets: "publish", Markdown: guideText("publish")},
		{ID: "generic.maintain", Topic: formats.GuideMaintain, Title: "Replace and delete files", Classes: local, Markdown: guideText("maintain")},
		{ID: "generic.endpoint", Topic: formats.GuideEndpoint, Title: "On this endpoint", Markdown: guideText("endpoint")},
		{ID: "generic.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
	}
}

// guideText is the template of one section; a missing file is a defect of the
// build, which the conformance kit reports as a required topic that says nothing.
func guideText(name string) string {
	b, err := guideFiles.ReadFile("guide/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(b)
}
