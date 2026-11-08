package oci

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is the guide of the OCI format (R-OCI-030, api-and-ui.md R-UI-049): what a local, a remote and a virtual repository is, and how
// the Docker CLI and Podman sign in, pull, push (a multi-platform image too), delete, and what the Kubernetes image pull secret is, what
// the Docker Engine mirror of a bound host changes, why a Docker Hub remote should be authenticated, and what each answer of the
// registry means. Every statement describes trove as the requirements define it; guide_test.go pins the facts the text names to the code.
func (f *Format) Guide() []formats.GuideSection {
	return supportedOnly(f.Descriptor(), guideSections())
}

// guideSections are all the sections, written for every class the format will have.
func guideSections() []formats.GuideSection {
	local, remote, virtual := []formats.Class{formats.Local}, []formats.Class{formats.Remote}, []formats.Class{formats.Virtual}
	writable := []formats.Class{formats.Local, formats.Virtual}
	return []formats.GuideSection{
		{ID: "oci.local.what", Topic: formats.GuideWhat, Title: "What a local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "oci.remote.what", Topic: formats.GuideWhat, Title: "What a remote repository is", Classes: remote, Markdown: guideText("what-remote")},
		{ID: "oci.virtual.what", Topic: formats.GuideWhat, Title: "What a virtual repository is", Classes: virtual, Markdown: guideText("what-virtual")},
		{ID: "oci.configure", Topic: formats.GuideConfigure, Title: "Sign the client in", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "oci.authenticate", Topic: formats.GuideAuthenticate, Title: "Which credential the client sends", Markdown: guideText("authenticate")},
		{ID: "oci.install", Topic: formats.GuideInstall, Title: "Pull an image", Snippets: "install", Markdown: guideText("install")},
		{ID: "oci.publish", Topic: formats.GuidePublish, Title: "Push an image", Classes: writable, Snippets: "publish", Markdown: guideText("publish")},
		{ID: "oci.maintain", Topic: formats.GuideMaintain, Title: "Delete tags, images and layers", Classes: local, Markdown: guideText("maintain")},
		{ID: "oci.endpoint", Topic: formats.GuideEndpoint, Title: "On this endpoint", Markdown: guideText("endpoint")},
		{ID: "oci.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
	}
}

// supportedOnly keeps, of each section, the classes the format registers: the kit refuses a section written for a class the descriptor
// does not list (check 8), and the remote and the virtual class join the descriptor with their adapters. The sections of those classes
// are written now and appear with the class, without an edit here.
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
