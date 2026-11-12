//go:build e2e

package e2e

// The local class of the Debian format with the real clients (R-DEB-035, R-DEB-031, R-DEB-006, R-DEB-016, R-DEB-018): a package is
// uploaded with curl from the pod of each image, the index is waited for with the index state read, and apt-get update --error-on=any and
// apt-get install run against the repository with the setup snippet's source and key. Every request of a client may reach another
// packages replica (the scatter profile), and the signatures are checked by the verifier of the image (sqv or gpgv).

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDebLocal(t *testing.T) {
	fx := debFx(t)
	repo := fx.localRepo(t, "local", nil)
	other := fx.localRepo(t, "other", nil)

	for _, img := range aptImages {
		t.Run("R-DEB-035 R-DEB-006 R-DEB-016 "+img.name+" uploads with curl, apt-get update --error-on=any and apt-get install through deb-local", func(t *testing.T) {
			p := newDebPackage(t, fx.name(img.name), "1.0")
			c := fx.client(t, img)
			aptWrite(t, img, "/tmp/"+p.file, p.data)

			// the upload of the spec (R-DEB-006), with curl in the pod of the image
			out := c.must(fmt.Sprintf(`curl -fsS -u "trove:$TROVE_TOKEN" --upload-file /tmp/%s %s`, p.file,
				shellQuoteWord(fmt.Sprintf("%supload/%s?distribution=%s&component=main", debRepoURL(t, repo), p.file, debSuite))))
			seq := uploadSeq(t, []byte(out), debSuite)
			state := fx.awaitPublished(t, repo, debSuite, seq)
			if state.State != "current" || len(state.SignedBy) != 1 {
				t.Errorf("the suite after the upload: %+v, want state current signed by one key", state)
			}

			c.configure(repo)
			// the credential is a file only root reads, the source names the key file, nothing turns the verification off
			if mode := strings.TrimSpace(c.must(`stat -c %a ` + debAuthFile(repo))); mode != "600" {
				t.Errorf("%s has the mode %s, want 600", debAuthFile(repo), mode)
			}
			if src := c.must(`cat ` + debSourceFile(repo)); !strings.Contains(src, "Signed-By: "+debKeyFile(repo)) || regexp.MustCompile(`(?i)trusted|allow-insecure`).MatchString(src) {
				t.Errorf("the source file is not a verified source:\n%s", src)
			}
			c.mustUpdate()
			c.mustInstallAndRun(p.name+"="+p.version, p.name, p.line())
		})

		t.Run("R-DEB-035 R-DEB-016 "+img.name+" refuses a keyring that holds another key, and accepts the repository's key again", func(t *testing.T) {
			p := newDebPackage(t, fx.name(img.name+"-wk"), "1.0")
			fx.awaitPublished(t, repo, debSuite, fx.upload(t, repo, p, debSuite))
			c := fx.client(t, img)
			c.configure(repo)
			aptWrite(t, img, debKeyFile(repo), fx.signingKeyFile(t, other))
			c.mustFailVerification("a keyring with the key of another repository")
			aptWrite(t, img, debKeyFile(repo), fx.signingKeyFile(t, repo))
			c.mustUpdate()
		})
	}

	t.Run("R-DEB-018 an anonymous request for the key of a repository that requires authentication is refused", func(t *testing.T) {
		newAPI(t).do(http.MethodGet, debRepoPath(repo)+"/signing-key.asc", nil).want(t, "anonymous signing-key.asc", 401)
	})
}

// TestDebAnonymous is the anonymous read: the repository has public artifacts (R-AUTHZ-033), the clients carry no credential file, and
// apt-get update and install work on every image.
func TestDebAnonymous(t *testing.T) {
	fx := debFx(t)
	repo := fx.localRepo(t, "open", nil)
	fx.withAnonymousRead(t, repo)
	p := newDebPackage(t, fx.name("anon"), "1.0")
	fx.awaitPublished(t, repo, debSuite, fx.upload(t, repo, p, debSuite))

	for _, img := range aptImages {
		t.Run("R-DEB-035 R-DEB-004 "+img.name+" reads an anonymous repository without a credential", func(t *testing.T) {
			c := fx.client(t, img)
			c.configure(repo)
			if out, err := c.sh(`test -e ` + debAuthFile(repo)); err == nil {
				t.Fatalf("the configure snippet of a repository without authentication wrote a credential file:\n%s", out)
			}
			c.mustUpdate()
			c.mustInstallAndRun(p.name+"="+p.version, p.name, p.line())
		})
	}
}

// withAnonymousRead waits until every packages replica lets an anonymous client read the repositories (R-AUTHZ-033). The repositories
// are public (localRepo with the purpose "open"); nothing else of the installation is.
func (fx *debFixture) withAnonymousRead(t *testing.T, keys ...string) {
	t.Helper()
	// wait until every packages replica lets an anonymous client read
	seen := map[string]bool{}
	waitFor(t, "anonymous read of "+strings.Join(keys, ", ")+" on every packages replica", time.Minute, time.Second, func() bool {
		clear(seen)
		for range 9 {
			for _, key := range keys {
				r, err := newAPI(t).send(http.MethodGet, debRepoPath(key)+"/signing-key.asc", nil)
				if err != nil || r.Status != 200 {
					return false
				}
				seen[r.Replica()] = true
			}
		}
		return len(seen) >= 3
	})
}
