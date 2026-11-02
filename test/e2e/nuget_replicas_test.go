//go:build e2e

package e2e

// NuGet on interchangeable replicas (R-NUGET-030, exit criterion E3.6): every rendered document has the same body and
// the same weak ETag on every web replica of the scatter profile, and a conditional request is answered 304 by any of them.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestNuGetReplicas(t *testing.T) {
	fx := nugetFx(t)
	auth := nugetFeed(t, "localhost", nugetAuthKey)
	id := fx.pkgID("Replicas")
	c := fx.client(t, 0, "replicas-setup")
	c.config(auth, true)
	for _, v := range []string{"1.0.0", "1.1.0"} {
		file := c.place(id, v, fx.nugetPack(t, id, v, "replicas "+v))
		c.mustPush("push "+v, "dotnet", "nuget", "push", file, "--source", auth, "--api-key", "az")
	}
	lower := strings.ToLower(id)
	base := "/api/nuget/" + nugetAuthKey + "/"
	// path -> the ETag it carries: a rendered document a weak one (R-NUGET-030, Q-NUGET-9), a stored file (the .nuspec)
	// the strong ETag of its blob, the search none (volatile)
	docs := map[string]string{
		base + "v3/index.json":                                             "weak",
		base + "v3/flatcontainer/" + lower + "/index.json":                 "weak",
		base + "v3/registration/" + lower + "/index.json":                  "weak",
		base + "v3/registration-gz/" + lower + "/index.json":               "weak",
		base + "v3/registration-semver2/" + lower + "/index.json":          "weak",
		base + "v3/registration-semver2/" + lower + "/1.1.0.json":          "weak",
		base + "v3/flatcontainer/" + lower + "/1.0.0/" + lower + ".nuspec": "strong",
		base + "v3/search?q=" + lower + "&take=5":                          "none",
	}
	for path, kind := range docs {
		t.Run("R-NUGET-030 E3.6 "+strings.TrimPrefix(path, base), func(t *testing.T) {
			type seen struct{ body, etag string }
			byReplica := map[string]seen{}
			for range 60 {
				r := fx.adm.get(path).want(t, path, 200)
				sum := sha256.Sum256(r.Body)
				s := seen{body: hex.EncodeToString(sum[:]), etag: r.Header.Get("ETag")}
				if kind == "weak" && !strings.HasPrefix(s.etag, `W/"`) || kind == "strong" && !strings.HasPrefix(s.etag, `"`) {
					t.Fatalf("%s on %s: ETag %q is not a %s ETag", path, r.Replica(), s.etag, kind)
				}
				for other, o := range byReplica {
					if o != s {
						t.Fatalf("%s: %s answered %+v, %s answered %+v", path, r.Replica(), s, other, o)
					}
				}
				byReplica[r.Replica()] = s
				if len(byReplica) == 3 {
					break
				}
			}
			if len(byReplica) < 3 {
				t.Fatalf("%s: only %d web replica(s) answered in 60 requests; the scatter profile reaches three", path, len(byReplica))
			}
			if kind == "none" {
				return
			}
			// a conditional request is answered by any replica with 304
			var etag string
			for _, s := range byReplica {
				etag = s.etag
			}
			replicas := map[string]bool{}
			for range 60 {
				r := fx.adm.do(http.MethodGet, path, nil, "If-None-Match", etag).want(t, path+" with If-None-Match", 304)
				replicas[r.Replica()] = true
				if len(replicas) == 3 {
					return
				}
			}
			t.Fatalf("%s: the conditional requests reached %d replicas", path, len(replicas))
		})
	}
}
