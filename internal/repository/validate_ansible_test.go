package repository

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

func ansibleDoc(class Class, authURL, remote string) *Document {
	d := &Document{Class: class, Format: "ansible"}
	member, _ := json.Marshal(map[string]any{"authUrl": authURL})
	d.Ansible = member
	if remote != "" {
		d.Remote = json.RawMessage(remote)
	}
	return d
}

func TestR_ANS_019_TheTokenEndpointOfARealmIsHeldToWhatTheEngineEnforcesAgain(t *testing.T) {
	entry := catalog.Entry{Facts: catalog.Facts{ID: "ansible"}}
	const hubRemote = `{"url":"https://hub.example/api/","authMode":"bearer","extraHosts":[{"pattern":"sso.example","sendCredentials":true},{"pattern":"cdn.example","sendCredentials":false}]}`
	for _, tc := range []struct {
		name, authURL, remote string
		bad                   bool
	}{
		{"an https realm that may have the credential", "https://sso.example/auth/token", hubRemote, false},
		{"the upstream's own host", "https://hub.example/token", hubRemote, false},
		{"http on a loopback address", "http://127.0.0.1:8080/token", `{"url":"https://127.0.0.1:8080/api/","authMode":"bearer"}`, false},
		{"http on a loopback name", "http://sso.localhost:8080/token", `{"url":"https://x.example/api/","authMode":"bearer","extraHosts":[{"pattern":"sso.localhost","sendCredentials":true}]}`, false},
		{"plain http on a public host", "http://sso.example/token", hubRemote, true},
		{"an extra host without sendCredentials", "https://cdn.example/token", hubRemote, true},
		{"a host the repository does not list", "https://elsewhere.example/token", hubRemote, true},
		{"a repository whose credential is not a bearer token", "https://sso.example/token", `{"url":"https://hub.example/api/","authMode":"basic","extraHosts":[{"pattern":"sso.example","sendCredentials":true}]}`, true},
		{"user information", "https://u:p@sso.example/token", hubRemote, true},
		{"a query", "https://sso.example/token?x=1", hubRemote, true},
		{"no host", "https:///token", hubRemote, true},
		{"not a URL", "sso", hubRemote, true},
	} {
		v := &validation{}
		validateAnsible(v, entry, ansibleDoc(Remote, tc.authURL, tc.remote))
		if got := len(v.fields) != 0; got != tc.bad {
			t.Errorf("%s: refused %v (%+v), want %v", tc.name, got, v.fields, tc.bad)
		}
		for _, f := range v.fields {
			if f.Pointer != "/ansible/authUrl" || f.Code != codeInvalidValue {
				t.Errorf("%s: %+v", tc.name, f)
			}
		}
	}
	// Nothing to check without an authUrl, for another class or another format.
	for _, d := range []*Document{ansibleDoc(Remote, "", hubRemote), ansibleDoc(Local, "http://evil.example/", ""), {Class: Remote, Format: "ansible"}} {
		v := &validation{}
		validateAnsible(v, entry, d)
		if len(v.fields) != 0 {
			t.Errorf("%+v: %+v", d, v.fields)
		}
	}
	v := &validation{}
	validateAnsible(v, catalog.Entry{Facts: catalog.Facts{ID: "npm"}}, ansibleDoc(Remote, "http://evil.example/", ""))
	if len(v.fields) != 0 {
		t.Errorf("another format: %+v", v.fields)
	}
}
