package formattest_test

import "github.com/guided-traffic/trove/internal/formats/catalog"

func catalogFacts() catalog.Facts {
	return catalog.Facts{
		ID: "barefmt", DisplayName: "Bare", Classes: []catalog.Class{catalog.Local}, Layout: "barefmt",
		ProtocolBasePath: "/files/{key}/", PatternSubject: catalog.SubjectPath,
	}
}
