package deb

import "github.com/guided-traffic/trove/internal/formats"

// virtualGuideSections are the sections the virtual class adds to the guide (R-DEB-029): what a virtual repository is and what its index
// lists, and what the state of a build says about a member. The configure, authenticate, install and publish sections of the local class
// apply to it too: the client adds the same kind of source and trusts the virtual repository's own key.
func virtualGuideSections() []formats.GuideSection {
	virtual := []formats.Class{formats.Virtual}
	return []formats.GuideSection{
		{ID: "deb.virtual.what", Topic: formats.GuideWhat, Title: "What a virtual repository is", Classes: virtual, Markdown: guideText("what-virtual")},
		{ID: "deb.virtual.maintain", Topic: formats.GuideMaintain, Title: "Members and what a build says about them", Classes: virtual, Markdown: guideText("maintain-virtual")},
	}
}
