package ansible

import (
	"encoding/json"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// upstreamHost is the origin of the remote repository of the kit's check 4.
const upstreamHost = "https://galaxy.upstream.test"

const sha256Zero = "0000000000000000000000000000000000000000000000000000000000000000"

// remoteFixture is the fixture of check 4: the stored documents of a remote repository as the upstream serves them. Their addresses
// are on the upstream's host, and none may be left in the rendered document.
func remoteFixture() *formattest.RemoteFixture {
	const idx = "/api/v3/plugin/ansible/content/published/collections/index/acme/tools/"
	const idxHost = upstreamHost + idx
	version := `{"version":"1.0.0","href":"` + idxHost + `versions/1.0.0/","created_at":"2026-10-01T10:00:00.000000Z","updated_at":"2026-10-01T10:00:00.000000Z",` +
		`"requires_ansible":">=2.14.0","marks":[],"artifact":{"filename":"acme-tools-1.0.0.tar.gz","sha256":"` + sha256Zero + `","size":1234},` +
		`"collection":{"id":"11111111-2222-3333-4444-555555555555","name":"tools","href":"` + idxHost + `"},` +
		`"download_url":"` + upstreamHost + `/api/v3/plugin/ansible/content/published/collections/artifacts/acme-tools-1.0.0.tar.gz","name":"tools",` +
		`"namespace":{"name":"acme"},"signatures":[{"signature":"opaque"}],` +
		`"metadata":{"authors":["a"],"dependencies":{"acme.base":">=1.0.0"},"description":"d","contents":[{"name":"x"}],"docs_blob":{},"manifest":{},"files":[]}}`
	versions := `{"meta":{"count":3},"links":{"first":"` + idxHost + `versions/?limit=100&offset=0","previous":null,"next":"` + idxHost + `versions/?limit=100&offset=100","last":"` + idxHost + `versions/?limit=100&offset=200"},` +
		`"data":[{"version":"1.1.0","href":"` + idxHost + `versions/1.1.0/","created_at":"2026-10-02T10:00:00.000000Z","updated_at":"2026-10-02T10:00:00.000000Z","requires_ansible":">=2.14.0","marks":[]},` +
		`{"version":"1.0.0","href":"` + idxHost + `versions/1.0.0/","created_at":"2026-10-01T10:00:00.000000Z","updated_at":"2026-10-01T10:00:00.000000Z","requires_ansible":null,"marks":[]}]}`
	collectionDoc := `{"href":"` + idxHost + `","namespace":"acme","name":"tools","deprecated":false,"versions_url":"` + idxHost + `versions/",` +
		`"highest_version":{"href":"` + idxHost + `versions/1.1.0/","version":"1.1.0"},"created_at":"2026-10-01T10:00:00.000000Z","updated_at":"2026-10-02T10:00:00.000000Z"}`
	rolePage := `{"count":1,"next":null,"previous":null,"next_link":"/api/v1/roles/16/versions/?page=2&page_size=50","previous_link":null,"cur_page":1,"num_pages":2,"page_size":50,` +
		`"results":[{"id":160,"name":"1.0.0","download_url":"https://github.com/acme/ansible-role-tools/archive/1.0.0.tar.gz"}]}`
	lookup := `{"count":1,"next":null,"previous":null,"next_link":null,"previous_link":null,"results":[{"id":16,"url":"` + upstreamHost + `/api/v1/roles/16/","name":"tools",` +
		`"related":{"versions":"` + upstreamHost + `/api/v1/roles/16/versions/"},"github_user":"acme","github_repo":"ansible-role-tools"}]}`
	return &formattest.RemoteFixture{
		Upstream: upstreamHost,
		Docs: []formattest.RemoteDoc{
			{Name: "version", Path: "collections/acme/tools/1.0.0.json", Request: "v3/collections/acme/tools/versions/1.0.0/", Variant: documentVariant, Body: []byte(version)},
			{Name: "version list", Path: "collections/acme/tools/versions.json", Request: "v3/collections/acme/tools/versions/", Variant: "offset=0", Body: []byte(versions)},
			{Name: "collection", Path: "collections/acme/tools/collection.json", Request: "v3/collections/acme/tools/", Variant: documentVariant, Body: []byte(collectionDoc)},
			{Name: "role versions", Path: "roles/16/versions.json", Request: "v1/roles/16/versions/", Variant: "page=1", Body: []byte(rolePage)},
			{Name: "role lookup", Path: "roles/lookup.json", Request: "v1/roles/", Variant: "owner=acme&name=tools", Body: []byte(lookup)},
		},
		DownloadLinks: func(doc []byte) []string {
			var out []string
			var walk func(v any)
			walk = func(v any) {
				switch x := v.(type) {
				case map[string]any:
					for k, e := range x {
						if s, ok := e.(string); ok && (k == "download_url" || k == "href" || k == "url" || k == "versions" || k == "first" || k == "last" || k == "next" || k == "next_link") {
							out = append(out, s)
							continue
						}
						walk(e)
					}
				case []any:
					for _, e := range x {
						walk(e)
					}
				}
			}
			var v any
			if json.Unmarshal(doc, &v) == nil {
				walk(v)
			}
			return out
		},
	}
}

// discoveryFixture are the API roots of check 6 (R-ANS-016): the one the public Galaxy serves, one without v1 (a hub), and crafted
// ones that must be refused - a prefix with a scheme, one that leaves the API root, one that is absolute, no v3 API, no object.
func discoveryFixture() []formattest.DiscoveryCase {
	root := func(avail string) []byte { return []byte(`{"available_versions":` + avail + `}`) }
	return []formattest.DiscoveryCase{
		{Name: "public galaxy", Doc: root(`{"v3":"v3/","v1":"v1/"}`), Valid: true},
		{Name: "collections only", Doc: root(`{"v3":"v3/"}`), Valid: true},
		{Name: "prefix without a slash", Doc: root(`{"v3":"v3"}`), Valid: true},
		{Name: "prefix with a scheme", Doc: root(`{"v3":"https://evil.example/v3/"}`)},
		{Name: "prefix that leaves the root", Doc: root(`{"v3":"../v3/"}`)},
		{Name: "absolute prefix", Doc: root(`{"v3":"/v3/"}`)},
		{Name: "prefix with a query", Doc: root(`{"v3":"v3/?x=1"}`)},
		{Name: "no v3", Doc: root(`{"v1":"v1/"}`)},
		{Name: "not an object", Doc: []byte(`["v3"]`)},
		{Name: "available_versions of the wrong type", Doc: root(`["v3"]`)},
		{Name: "deeply nested", Doc: []byte(`{"available_versions":{"v3":"v3/"},"x":` + strings.Repeat("[", 100000) + strings.Repeat("]", 100000) + `}`)},
	}
}
