package repository

import (
	"reflect"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// credentialCatalog proposes a token realm host with credentials and a blob host without, as a container registry format does.
func credentialCatalog() *catalog.Registry {
	r := testCatalog()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: "regfmt", Classes: []Class{Local, Remote, Virtual}, Layout: "regfmt", ProtocolBasePath: "/api/regfmt/{project}/{key}/",
		PatternSubject: SubjectName, PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
		DefaultUpstreamURL:     "https://registry.example.org/",
		DefaultExtraHosts:      []string{"auth.registry.example.org", "*.blobs.example.org"},
		DefaultCredentialHosts: []string{"auth.registry.example.org"},
	}, Names: lower{}})
	return r
}

func credentialSettings(t *testing.T, remote string) RemoteSettings {
	t.Helper()
	body := `{"key":"reg-remote","class":"remote","format":"regfmt"`
	if remote != "" {
		body += `,"remote":` + remote
	}
	d, err := DecodeDocument([]byte(body + `}`))
	if err != nil {
		t.Fatal(err)
	}
	_, v := validate(credentialCatalog(), &d, true)
	if err := v.err(); err != nil {
		t.Fatal(err)
	}
	return settingsOf(t, d)
}

// TestR_REPO_024_DefaultCredentialHostsAreProposedWithSendCredentials: for the format's well-known upstream the proposed extra
// hosts come with sendCredentials true exactly for the hosts the format names as credential hosts (R-REPO-033, R-REPO-024); any
// other upstream, and an explicit list, take no proposal.
func TestR_REPO_024_DefaultCredentialHostsAreProposedWithSendCredentials(t *testing.T) {
	t.Run("the well-known upstream", func(t *testing.T) {
		want := []ExtraHost{{Pattern: "auth.registry.example.org", SendCredentials: true}, {Pattern: "*.blobs.example.org"}}
		if got := credentialSettings(t, "").ExtraHosts; !reflect.DeepEqual(got, want) {
			t.Errorf("extraHosts = %+v, want %+v", got, want)
		}
	})
	t.Run("another upstream takes no proposal", func(t *testing.T) {
		if got := credentialSettings(t, `{"url":"https://registry.partner.example/"}`).ExtraHosts; len(got) != 0 {
			t.Errorf("extraHosts = %+v", got)
		}
	})
	t.Run("an explicit list is kept as given", func(t *testing.T) {
		got := credentialSettings(t, `{"extraHosts":[{"pattern":"auth.registry.example.org","sendCredentials":false}]}`).ExtraHosts
		want := []ExtraHost{{Pattern: "auth.registry.example.org"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("extraHosts = %+v, want %+v", got, want)
		}
	})
}
