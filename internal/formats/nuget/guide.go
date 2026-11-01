package nuget

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is NuGet's guide (R-NUGET-026, api-and-ui.md R-UI-049, Q-NUGET-50): what a local, a
// remote and a virtual repository is, and how the .NET CLI, nuget.exe and Visual Studio
// configure, sign in, install, push, unlist, relist and delete, keep lock files and are
// protected against dependency confusion through one, what an endpoint with exposure rules
// changes, and what each answer of the protocol means. Every statement describes trove as the
// requirements define it; the tests in guide_test.go pin the facts the text names to the code.
//
// Visual Studio has no command to push, unlist, relist or delete, so the publish and the
// maintain sections are written for the two command line clients only.
func (f *Format) Guide() []formats.GuideSection {
	return supportedOnly(f.Descriptor(), guideSections())
}

// guideSections are all the sections, written for every class the format will have.
func guideSections() []formats.GuideSection {
	local, remote, virtual := []formats.Class{formats.Local}, []formats.Class{formats.Remote}, []formats.Class{formats.Virtual}
	writable := []formats.Class{formats.Local, formats.Virtual}
	commandLine := []string{ClientDotnet, ClientNuGet}
	return []formats.GuideSection{
		{ID: "nuget.local.what", Topic: formats.GuideWhat, Title: "What a local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "nuget.remote.what", Topic: formats.GuideWhat, Title: "What a remote repository is", Classes: remote, Markdown: guideText("what-remote")},
		{ID: "nuget.virtual.what", Topic: formats.GuideWhat, Title: "What a virtual repository is", Classes: virtual, Markdown: guideText("what-virtual")},
		{ID: "nuget.configure", Topic: formats.GuideConfigure, Title: "Point the client at the repository", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "nuget.authenticate", Topic: formats.GuideAuthenticate, Title: "Sign the client in", Markdown: guideText("authenticate")},
		{ID: "nuget.install", Topic: formats.GuideInstall, Title: "Restore and install packages", Snippets: "install", Markdown: guideText("install")},
		{ID: "nuget.publish", Topic: formats.GuidePublish, Title: "Push a package", Classes: writable, Clients: commandLine, Snippets: "publish", Markdown: guideText("publish")},
		{ID: "nuget.maintain", Topic: formats.GuideMaintain, Title: "Unlist, relist and delete", Classes: writable, Clients: commandLine, Markdown: guideText("maintain")},
		{ID: "nuget.lockfiles", Topic: formats.GuideLockfiles, Title: "Lock files", Markdown: guideText("lockfiles")},
		{ID: "nuget.endpoint", Topic: formats.GuideEndpoint, Title: "On this endpoint", Markdown: guideText("endpoint")},
		{ID: "nuget.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
	}
}

// supportedOnly keeps, of each section, the classes the format registers: the kit refuses a
// section written for a class the descriptor does not list (check 8), and the remote and the
// virtual class join the descriptor with their adapters (Q-NUGET-28). The sections of those
// classes are written now and appear with the class, without an edit here.
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

// guideText is the template of one section; a missing file is a defect of the build, which the
// conformance kit reports as a required topic that says nothing.
func guideText(name string) string {
	b, err := guideFiles.ReadFile("guide/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(b)
}
