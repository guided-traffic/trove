package npm

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is npm's guide (R-NPM-041, api-and-ui.md R-UI-049): what a local, a remote
// and a virtual repository is, and how npm, pnpm, Yarn Berry and Yarn Classic
// configure, sign in, install, publish, tag, unpublish, audit and keep lockfiles
// through one, what an endpoint with exposure rules changes, and what each answer
// of the protocol means. Every statement describes trove as built; the tests in
// guide_test.go pin the facts the text names to the code.
func (f *Format) Guide() []formats.GuideSection {
	local, remote, virtual := []formats.Class{formats.Local}, []formats.Class{formats.Remote}, []formats.Class{formats.Virtual}
	writable := []formats.Class{formats.Local, formats.Virtual}
	return []formats.GuideSection{
		{ID: "npm.local.what", Topic: formats.GuideWhat, Title: "What a local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "npm.remote.what", Topic: formats.GuideWhat, Title: "What a remote repository is", Classes: remote, Markdown: guideText("what-remote")},
		{ID: "npm.virtual.what", Topic: formats.GuideWhat, Title: "What a virtual repository is", Classes: virtual, Markdown: guideText("what-virtual")},
		{ID: "npm.configure", Topic: formats.GuideConfigure, Title: "Point the client at the repository", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "npm.authenticate", Topic: formats.GuideAuthenticate, Title: "Sign the client in", Markdown: guideText("authenticate")},
		{ID: "npm.install", Topic: formats.GuideInstall, Title: "Install packages", Snippets: "install", Markdown: guideText("install")},
		{ID: "npm.publish", Topic: formats.GuidePublish, Title: "Publish a package", Classes: writable, Markdown: guideText("publish")},
		{ID: "npm.maintain", Topic: formats.GuideMaintain, Title: "Dist-tags and unpublish", Classes: writable, Markdown: guideText("maintain")},
		{ID: "npm.audit", Topic: formats.GuideAudit, Title: "Security audit", Markdown: guideText("audit")},
		{ID: "npm.lockfiles", Topic: formats.GuideLockfiles, Title: "Lockfiles", Markdown: guideText("lockfiles")},
		{ID: "npm.endpoint", Topic: formats.GuideEndpoint, Title: "On this endpoint", Markdown: guideText("endpoint")},
		{ID: "npm.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
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
