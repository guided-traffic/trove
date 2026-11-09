package deb

import "github.com/guided-traffic/trove/internal/formats"

// remoteGuideSections are the sections the remote class adds to the guide (R-DEB-029, api-and-ui.md R-UI-049): what a remote
// repository is and what it passes through, and which key the client trusts. They are written for the class and appear only where the
// format registers it.
func remoteGuideSections() []formats.GuideSection {
	remote := []formats.Class{formats.Remote}
	return []formats.GuideSection{
		{ID: "deb.remote.what", Topic: formats.GuideWhat, Title: "What a remote repository is", Classes: remote, Markdown: guideText("what-remote")},
		{ID: "deb.remote.keys", Topic: formats.GuideConfigure, Title: "Which key the client trusts", Classes: remote, Markdown: guideText("keys-remote")},
	}
}
