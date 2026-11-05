package upstream

import (
	"net/url"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestAllowedMirror: the hosts a mirror's discovery request may reach (R-REPO-051, Q-REPO-151): the upstream's, the extraHosts entries
// and the hosts the format's lister names; every other host is foreign_host_denied.
func TestAllowedMirror(t *testing.T) {
	origin, _ := url.Parse("https://registry.example.org/")
	c := &Client{key: "npm-mirror", origin: origin, settings: repository.RemoteSettings{
		ExtraHosts: []repository.ExtraHost{{Pattern: "*.cdn.example.org"}},
	}}
	hosts := []string{"replicate.example.org"}
	for _, tc := range []struct {
		name      string
		target    string
		hosts     []string
		viaMirror bool
		wantCode  string
	}{
		{name: "the upstream's host", target: "https://registry.example.org/-/all", hosts: hosts},
		{name: "an extraHosts entry", target: "https://a.cdn.example.org/x", hosts: hosts},
		{name: "a host the lister names", target: "https://replicate.example.org/registry/_all_docs", hosts: hosts, viaMirror: true},
		{name: "the lister's host in another case", target: "https://Replicate.Example.org/x", hosts: hosts, viaMirror: true},
		{name: "a host nobody named", target: "https://evil.example.net/x", hosts: hosts, wantCode: errs.CodeForeignHostDenied},
		{name: "the lister's host without the lister naming it", target: "https://replicate.example.org/x", wantCode: errs.CodeForeignHostDenied},
		{name: "a suffix of a named host", target: "https://x.replicate.example.org/x", hosts: hosts, wantCode: errs.CodeForeignHostDenied},
		{name: "a named host over http while the upstream is https", target: "http://replicate.example.org/x", hosts: hosts, wantCode: errs.CodeInsecureRedirect},
		{name: "the upstream's own host is never a discovery host", target: "https://registry.example.org/x", hosts: []string{"registry.example.org"}},
	} {
		t.Run("R-REPO-051 "+tc.name, func(t *testing.T) {
			u, _ := url.Parse(tc.target)
			via, err := c.AllowedMirror(u, tc.hosts)
			if tc.wantCode != "" {
				if ee, ok := errs.As(err); !ok || ee.Code != tc.wantCode {
					t.Fatalf("err = %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil || via != tc.viaMirror {
				t.Fatalf("via = %v, err = %v", via, err)
			}
		})
	}
}
