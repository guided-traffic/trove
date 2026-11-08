package repository

import (
	"encoding/json"
	"testing"
)

// R-OCI-026: the rules of an Owner's write reach the member: a write of a remote repository of the format that changes oci.mirror
// answers mirror_restricted at /oci/mirror, one that keeps it does not, and an administrator is not restricted.
func TestR_OCI_026_AnOwnersWriteOfAnOciRemoteCannotChangeTheMirrorMember(t *testing.T) {
	remote := json.RawMessage(`{"url":"https://registry-1.docker.io/","mode":"pullThrough"}`)
	doc := func(member string) *Document {
		d := &Document{Class: Remote, Format: "oci"}
		d.Remote = remote
		d.OCI = canonicalObject(json.RawMessage(member))
		return d
	}
	const (
		tags   = `{"mirror":{"tags":["latest"]}}`
		widest = `{"mirror":{"tags":["*"]}}`
	)
	for _, tc := range []struct {
		name       string
		caller     Caller
		cur, nd    *Document
		restricted bool
	}{
		{"an Owner patches oci.mirror.tags", Caller{Restricted: true}, doc(tags), doc(widest), true},
		{"an Owner sends the stored member back", Caller{Restricted: true}, doc(tags), doc(tags), false},
		{"an administrator patches it", Caller{}, doc(tags), doc(widest), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &validation{}
			restrictedRemote(v, tc.caller, tc.cur, tc.nd)
			got := fieldCodes(v)["/oci/mirror"]
			if (got == "mirror_restricted") != tc.restricted || (got != "" && got != "mirror_restricted") {
				t.Errorf("violations %v, restricted = %v", fieldCodes(v), tc.restricted)
			}
		})
	}
}

// R-OCI-026, Q-OCI-22: the member `<format>.mirror` is kept from a project Owner like the patterns.
func TestR_OCI_026_AnOwnerKeepsTheFormatsMirrorSettings(t *testing.T) {
	doc := func(format, member string) *Document {
		d := &Document{Format: format}
		switch format {
		case "oci":
			d.OCI = canonicalObject(json.RawMessage(member))
		case "npm":
			d.NPM = canonicalObject(json.RawMessage(member))
		}
		return d
	}
	const (
		latest    = `{"mirror":{"tags":["latest"],"excludeTags":[],"platforms":[]}}`
		reordered = `{"mirror":{"platforms":[],"excludeTags":[],"tags":["latest"]}}`
		wider     = `{"mirror":{"tags":["latest","1.*"],"excludeTags":[],"platforms":[]}}`
	)
	for _, tc := range []struct {
		name     string
		cur, nd  *Document
		refusals int
	}{
		{"the stored member sent back is kept", doc("oci", latest), doc("oci", latest), 0},
		{"the same member in another key order is kept", doc("oci", latest), doc("oci", reordered), 0},
		{"a document without the member is kept", doc("oci", ""), doc("oci", ""), 0},
		{"a wider tag list is refused", doc("oci", latest), doc("oci", wider), 1},
		{"dropping the member is refused", doc("oci", latest), doc("oci", ""), 1},
		{"adding it to a repository that has none is refused", doc("oci", ""), doc("oci", latest), 1},
		{"a create that sets it is refused", nil, doc("oci", latest), 1},
		{"a create without it is kept", nil, doc("oci", ""), 0},
		{"other members of the format's settings are not the mirror's", doc("npm", `{"x":1}`), doc("npm", `{"x":2}`), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &validation{}
			checkDelegatedFormatMirror(v, tc.cur, tc.nd)
			got := fieldCodes(v)
			if len(got) != tc.refusals {
				t.Fatalf("violations %v, want %d", got, tc.refusals)
			}
			if tc.refusals == 1 && got["/"+tc.nd.Format+"/mirror"] != "mirror_restricted" {
				t.Errorf("violations %v, want mirror_restricted at /%s/mirror", got, tc.nd.Format)
			}
		})
	}
}
