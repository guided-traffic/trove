//go:build integration

package bastion

import (
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// steps is what a client run saw: the status of every request, in order, and the artifacts it received.
type steps struct {
	statuses  []int
	paths     []string
	artifacts int
	failed    []string
}

func (s *steps) add(p string, r reply) {
	s.statuses = append(s.statuses, r.Status)
	s.paths = append(s.paths, p)
	if r.Status != 200 {
		s.failed = append(s.failed, p)
	}
}

func (s steps) ok() bool { return len(s.failed) == 0 && len(s.statuses) > 0 }

func escapeNPM(name string) string { return strings.ReplaceAll(name, "/", "%2f") }

// npmRun is `npm ci` as the requests go: the package document of each name, then the tarball of the latest
// version at the address the document gives (which must be on the Bastion's host, R-BAS-017).
func npmRun(t *testing.T, r *replica, repo string, o callOpts, names ...string) steps {
	t.Helper()
	var s steps
	for _, name := range names {
		docPath := "/api/npm/" + repo + "/" + escapeNPM(name)
		doc := r.do(t, "GET", docPath, o)
		s.add(docPath, doc)
		if doc.Status != 200 {
			continue
		}
		var d struct {
			Tags     map[string]string `json:"dist-tags"`
			Versions map[string]struct {
				Dist struct {
					Tarball   string `json:"tarball"`
					Integrity string `json:"integrity"`
				} `json:"dist"`
			} `json:"versions"`
		}
		if err := json.Unmarshal(doc.Body, &d); err != nil {
			t.Fatalf("%s: %v\n%s", docPath, err, doc.Body)
		}
		v, ok := d.Versions[d.Tags["latest"]]
		if !ok {
			t.Fatalf("%s: no latest version in %s", docPath, doc.Body)
		}
		u, err := url.Parse(v.Dist.Tarball)
		if err != nil || u.Host != baseHost {
			t.Fatalf("%s: dist.tarball %q is not on %s", docPath, v.Dist.Tarball, baseHost)
		}
		tgz := r.do(t, "GET", u.Path, o)
		s.add(u.Path, tgz)
		if tgz.Status == 200 {
			sum := sha512.Sum512(tgz.Body)
			if want := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); v.Dist.Integrity != "" && want != v.Dist.Integrity {
				t.Fatalf("%s: the tarball does not match dist.integrity", u.Path)
			}
			s.artifacts++
		}
	}
	return s
}

// nugetRun is `dotnet restore` as the requests go: the service index, the flat container's version list and
// the .nupkg of the first version.
func nugetRun(t *testing.T, r *replica, repo string, o callOpts, ids ...string) steps {
	t.Helper()
	var s steps
	base := "/api/nuget/" + repo + "/"
	idx := base + "v3/index.json"
	res := r.do(t, "GET", idx, o)
	s.add(idx, res)
	if res.Status != 200 {
		return s
	}
	if strings.Contains(string(res.Body), mainHost) {
		t.Fatalf("the service index names the main installation's host:\n%s", res.Body)
	}
	for _, id := range ids {
		l := strings.ToLower(id)
		list := base + "v3/flatcontainer/" + l + "/index.json"
		lr := r.do(t, "GET", list, o)
		s.add(list, lr)
		if lr.Status != 200 {
			continue
		}
		var v struct {
			Versions []string `json:"versions"`
		}
		if err := json.Unmarshal(lr.Body, &v); err != nil || len(v.Versions) == 0 {
			t.Fatalf("%s: %v %s", list, err, lr.Body)
		}
		p := base + "v3/flatcontainer/" + l + "/" + v.Versions[0] + "/" + l + "." + v.Versions[0] + ".nupkg"
		pr := r.do(t, "GET", p, o)
		s.add(p, pr)
		if pr.Status == 200 {
			s.artifacts++
		}
	}
	return s
}

// tarballPath is the path of an @acme/sdk tarball.
func tarballPath(repo, name, version string) string {
	base := name[strings.LastIndex(name, "/")+1:]
	return "/api/npm/" + repo + "/" + name + "/-/" + base + "-" + version + ".tgz"
}

func nupkgPath(repo, id, version string) string {
	l := strings.ToLower(id)
	return "/api/nuget/" + repo + "/v3/flatcontainer/" + l + "/" + version + "/" + l + "." + version + ".nupkg"
}
