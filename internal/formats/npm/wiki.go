package npm

import (
	"context"
	"encoding/json"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/merge"
	"github.com/guided-traffic/trove/internal/formats/npm/name"
	"github.com/guided-traffic/trove/internal/formats/npm/remote"
)

// The README of a package is wiki content (R-AUTHZ-034, R-NPM-010, Q-NPM-87): `readme` and `readmeFilename` of the full document are
// present only for a caller who may read the wiki of the repository that supplies the version `latest` points to, asked through
// Host.WikiReadable. A caller who may not gets the rest of the document unchanged. The rendition key and the ETag of R-NPM-013 carry
// the answer, so a cached document with the README never reaches a caller without it. The abbreviated document and the version
// documents carry no README and are not affected.

// wikiBit is the part of a variant, and so of a key and a validator, that tells the answer of the wiki check apart.
func wikiBit(readable bool) string {
	if readable {
		return "1"
	}
	return "0"
}

// wikiReadableOf asks the Host whether the subject may read the wiki of the repository rq addresses, for the package.
func wikiReadableOf(ctx context.Context, rq *formats.Request, n name.Name) bool {
	return rq.Host.WikiReadable(ctx, rq, documentTarget(n, "", formats.ActionRead))
}

// membersWiki answers the wiki check for every member of a virtual repository, in the order of its member list: the string is an
// input of the validator and of the rendition key (the README comes from the member that supplies `latest`, which only the merge
// knows), and the function answers for one member by name. A member the request cannot be made for answers no.
func membersWiki(ctx context.Context, rq *formats.Request, n name.Name) (fingerprint string, readable func(member string) bool) {
	ms := rq.Repo.Members()
	byName := make(map[string]bool, len(ms))
	bits := make([]byte, 0, len(ms))
	for i := range ms {
		ok := false
		if mrq, err := rq.Member(&ms[i]); err == nil {
			ok = wikiReadableOf(ctx, mrq, n)
		}
		byName[ms[i].Name.String()] = ok
		bits = append(bits, wikiBit(ok)[0])
	}
	return string(bits), func(member string) bool { return byName[member] }
}

// takeReadmeMember removes the private member of a merged document that names the member the README comes from, and returns the
// name; empty when the document has none (a document that is not merged, or one without a README).
func takeReadmeMember(p *remote.Parsed) string {
	raw, ok := p.Top[merge.ReadmeMemberKey]
	if !ok {
		return ""
	}
	delete(p.Top, merge.ReadmeMemberKey)
	var member string
	if json.Unmarshal(raw, &member) != nil {
		return ""
	}
	return member
}

// dropReadme removes the README from a parsed document.
func dropReadme(p *remote.Parsed) {
	delete(p.Top, "readme")
	delete(p.Top, "readmeFilename")
	p.NoReadme = true // and the same members of every version manifest
}
