package ansible

import (
	"embed"

	"github.com/guided-traffic/trove/internal/formats"
)

// guideFiles are the Markdown templates of the guide, one file per section.
//
//go:embed guide/*.md
var guideFiles embed.FS

// Guide is the guide of the Ansible Galaxy format (R-ANS-025, api-and-ui.md R-UI-049): what a local, a remote and a virtual repository
// is, how ansible-galaxy is pointed at the repository, which credential it sends where, how a collection and a requirements.yml are
// installed, what roles are and are not, how a collection is published and removed, and what each answer of the server means. Every
// statement describes trove as the requirements define it.
//
// The sections of a class appear with the class: a section written for a class the format does not register is left out, because the
// kit refuses it (check 8).
func (f *Format) Guide() []formats.GuideSection {
	return supportedOnly(f.Descriptor(), guideSections())
}

// guideSections are all the sections, written for every class the format has.
func guideSections() []formats.GuideSection {
	local, remote, virtual := []formats.Class{formats.Local}, []formats.Class{formats.Remote}, []formats.Class{formats.Virtual}
	writable := []formats.Class{formats.Local, formats.Virtual}
	return []formats.GuideSection{
		{ID: "ansible.local.what", Topic: formats.GuideWhat, Title: "What a local repository is", Classes: local, Markdown: guideText("what-local")},
		{ID: "ansible.remote.what", Topic: formats.GuideWhat, Title: "What a remote repository is", Classes: remote, Markdown: guideText("what-remote")},
		{ID: "ansible.virtual.what", Topic: formats.GuideWhat, Title: "What a virtual repository is", Classes: virtual, Markdown: guideText("what-virtual")},
		{ID: "ansible.configure", Topic: formats.GuideConfigure, Title: "Point ansible-galaxy at the repository", Snippets: "configure", Markdown: guideText("configure")},
		{ID: "ansible.authenticate", Topic: formats.GuideAuthenticate, Title: "Which credential ansible-galaxy sends", Markdown: guideText("authenticate")},
		{ID: "ansible.install", Topic: formats.GuideInstall, Title: "Install collections and roles", Snippets: "install", Markdown: guideText("install")},
		{ID: "ansible.publish", Topic: formats.GuidePublish, Title: "Publish a collection", Classes: writable, Snippets: "publish", Markdown: guideText("publish")},
		{ID: "ansible.maintain", Topic: formats.GuideMaintain, Title: "Remove a version, import tasks and tombstones", Classes: local, Markdown: guideText("maintain")},
		{ID: "ansible.troubleshooting", Topic: formats.GuideTroubleshooting, Title: "Troubleshooting", Markdown: guideText("troubleshooting")},
	}
}

// supportedOnly keeps, of each section, the classes the format registers: the kit refuses a section written for a class the descriptor
// does not list (check 8), and the remote and the virtual class join the descriptor with their adapters.
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
