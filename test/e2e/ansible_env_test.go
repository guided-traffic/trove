//go:build e2e

package e2e

// The harness of the Ansible acceptance tests (R-ANS-032): the repositories of a test, a publisher with a token, the two ansible-galaxy
// clients of ansible_clients_test.go driven through `kubectl exec` in a clean home, the control client of the counting Galaxy test
// upstream, and the collections the tests build in the pod with `ansible-galaxy collection init` and `build`. The scenarios are in
// ansible_local_test.go, ansible_auth_test.go, ansible_remote_test.go, ansible_digest_test.go, ansible_snippets_test.go and, waiting for the
// classes they need, ansible_virtual_test.go and ansible_mirror_test.go. docs/developer/e2e.md describes the whole.
//
// Every request of a client may reach another packages replica (the scatter profile). The clients run the server of an ansible.cfg the
// test writes, or the setup snippets the setup API renders unchanged (ansible_snippets_test.go).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

const ansiblePublisher = "e2e-ansible" // the publisher: read, deploy and delete on every repository named ansible-*

// ansibleFixture is the installation the Ansible tests share: created once per run, idempotent over runs.
type ansibleFixture struct {
	adm   *api
	token string // the publisher's token
	run   string // six hex digits, part of every repository and collection name this run creates
}

var (
	ansibleFxOnce sync.Once
	ansibleFxVal  *ansibleFixture
)

// ansibleFx puts the cluster into the scatter profile (three packages replicas, every request to another one) and creates the publisher.
func ansibleFx(t testing.TB) *ansibleFixture {
	t.Helper()
	useScatter(t)
	ansibleFxOnce.Do(func() {
		adm := adminAPI(t)
		b := make([]byte, 3)
		_, _ = rand.Read(b)
		fx := &ansibleFixture{adm: adm, run: hex.EncodeToString(b)}
		ensureSA(t, adm, ansiblePublisher)
		// one policy for every repository of the suite (an instance-wide policy selects by <projectGlob>/<keyGlob>, R-AUTHZ-004), so that a
		// repository a test creates is readable as soon as the replicas know it
		ensurePolicy(t, adm, ansiblePublisher, []string{"ansible-*"}, [2]any{"sa:" + ansiblePublisher, []string{"read", "deploy", "delete"}})
		fx.token = freshSAToken(t, adm, ansiblePublisher, 4*3600, map[string]any{"inherit": true}).Token
		ansibleFxVal = fx
	})
	if ansibleFxVal == nil {
		t.Fatal("the Ansible fixture did not finish (see the first failure)")
	}
	return ansibleFxVal
}

// registry is a client of the edge with the publisher's token as a direct credential.
func (fx *ansibleFixture) registry(t testing.TB) *api { return newAPI(t).withToken(fx.token) }

// ansibleRepoPath is the path of a repository's address below the host: /api/ansible/<project>/<key>.
func ansibleRepoPath(key string) string { return "/api/ansible/" + repoRef(key) }

// ansibleRepoURL is the address an ansible.cfg names for the repository: the base URL of the edge, with the trailing slash of the snippets.
func ansibleRepoURL(t testing.TB, key string) string {
	return "https://" + ansibleEdge(t) + ansibleRepoPath(key) + "/"
}

// localRepo creates a local Ansible repository for one test; authentication is required unless the purpose starts with "open".
func (fx *ansibleFixture) localRepo(t testing.TB, purpose string) string {
	t.Helper()
	key := fmt.Sprintf("ansible-%s-%s", purpose, fx.run)
	fx.createRepo(t, key, map[string]any{"key": key, "class": "local", "format": "ansible"})
	return key
}

// remoteRepo creates a remote repository over the API root of the counting Galaxy test upstream. The two role hosts of the stub are extra
// hosts (the redirect of a role archive goes from the GitHub stand-in to the codeload host); the unlisted host is not.
func (fx *ansibleFixture) remoteRepo(t testing.TB, purpose string, remote map[string]any) string {
	t.Helper()
	key := fmt.Sprintf("ansible-%s-%s", purpose, fx.run)
	host := ansibleStubHost(t)
	r := map[string]any{
		"url": "https://" + host + "/api/",
		"extraHosts": []any{
			map[string]any{"pattern": ansibleStubSibling(host, "github"), "sendCredentials": false},
			map[string]any{"pattern": ansibleStubSibling(host, "codeload"), "sendCredentials": false},
		},
	}
	for k, v := range remote {
		r[k] = v
	}
	fx.createRepo(t, key, map[string]any{"key": key, "class": "remote", "format": "ansible", "remote": r})
	return key
}

// ansibleStubSibling is the name of another host of the stub: ansiblestub.<ns>.svc becomes ansiblestub-<role>.<ns>.svc.
func ansibleStubSibling(host, role string) string {
	first, rest, _ := strings.Cut(host, ".")
	return first + "-" + role + "." + rest
}

func (fx *ansibleFixture) createRepo(t testing.TB, key string, body map[string]any) {
	t.Helper()
	fx.adm.do(http.MethodPost, reposRoute, body).want(t, "create "+key, 201)
	// A repository that stays behind is asked by the shadowing check of every first publish into a local member, and the clients of a later
	// run must not meet it: the repository goes with the test.
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ansibleReadableOnEveryReplica(t, fx.token, key)
}

// ansibleReadableOnEveryReplica waits until each packages replica lets the token read the new repository (the bound of R-AUTHZ-028 plus one
// second for the poll and the request): the discovery document of the repository answers 200.
func ansibleReadableOnEveryReplica(t testing.TB, token string, keys ...string) {
	t.Helper()
	addrs, stop := packagesForwards(t)
	defer stop()
	const within = 10 * time.Second
	for i, addr := range addrs {
		a := apiAtPod(t, addr, token)
		for _, key := range keys {
			start := time.Now()
			for {
				r := a.getOverForward(ansibleRepoPath(key) + "/")
				if r.Status == 200 {
					break
				}
				if time.Since(start) > within {
					t.Fatalf("replica %d: repository %s answered %s after its creation, want 200 within %s (R-AUTHZ-028)", i, key, r, within)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// ── the clients ──────────────────────────────────────────────────────────────

// ansibleClient is one scenario in the pod of one image. Every command runs in a clean home (trove-clean), so there is nothing to reset.
type ansibleClient struct {
	t   testing.TB
	img ansibleImage
	fx  *ansibleFixture
}

func (fx *ansibleFixture) client(t testing.TB, img ansibleImage) *ansibleClient {
	return &ansibleClient{t: t, img: img, fx: fx}
}

// sh runs a shell script in a clean home with the publisher's token as $TROVE_TOKEN and the environment given.
func (c *ansibleClient) sh(script string, env ...string) (string, error) {
	c.t.Helper()
	return ansibleTry(c.t, c.img, append([]string{"TROVE_TOKEN=" + c.fx.token}, env...), script)
}

func (c *ansibleClient) must(script string, env ...string) string {
	c.t.Helper()
	out, err := c.sh(script, env...)
	if err != nil {
		c.t.Fatalf("%s: %v\n%s\n%s", c.img.name, err, script, tailLines(out, 40))
	}
	return out
}

// ansibleCfg is the ansible.cfg of one server named trove; extra lines (a token, a username) follow the url.
func ansibleCfg(url string, extra ...string) string {
	return "[galaxy]\nserver_list = trove\n\n[galaxy_server.trove]\nurl=" + url + "\n" + strings.Join(extra, "\n") + "\n"
}

// withCfg is a script that first writes an ansible.cfg into the clean home.
func withCfg(cfg, script string) string {
	return "printf %s " + shellQuoteWord(cfg) + " >ansible.cfg\n" + script
}

// tokenEnvVar is the variable the client reads the token of the server trove from (R-ANS-025, Q-ANS-22).
const tokenEnvVar = "ANSIBLE_GALAXY_SERVER_TROVE_TOKEN"

// collection is a collection the tests publish: built in the pod with ansible-galaxy collection init and build.
type collection struct {
	ns, name, version string
	deps              map[string]string
	// salt changes the content of the archive without changing its identity: a re-publish with other bytes
	salt string
	// extra is a shell fragment run in the collection's source tree ($d) after the README is written: more files for the archive
	extra string
}

func (c collection) full() string    { return c.ns + "." + c.name }
func (c collection) archive() string { return fmt.Sprintf("%s-%s-%s.tar.gz", c.ns, c.name, c.version) }

// buildScript builds the archive into ./out/ of the clean home. The galaxy.yml is the one init writes, with the version and the
// dependencies of the collection; the README carries the salt, so a collection with another salt has other bytes.
func (c collection) buildScript() string {
	deps := make([]string, 0, len(c.deps))
	for k, v := range c.deps {
		deps = append(deps, fmt.Sprintf("%q: %q", k, v))
	}
	return fmt.Sprintf(`d=src/%[1]s/%[2]s
ansible-galaxy collection init %[1]s.%[2]s --init-path src >/dev/null
sed -i 's/^version:.*/version: %[3]s/; s/^dependencies:.*/dependencies: {%[4]s}/' $d/galaxy.yml
printf '%%s\n' %[5]s >$d/README.md
%[6]s
mkdir -p out
ansible-galaxy collection build $d --output-path out --force >/dev/null
`, c.ns, c.name, c.version, strings.Join(deps, ", "), shellQuoteWord("e2e collection "+c.full()+" "+c.salt), c.extra)
}

// publishScript builds and publishes the archive to the server of the ansible.cfg (the token comes from the environment).
func (c collection) publishScript() string {
	return c.buildScript() + "ansible-galaxy collection publish out/" + c.archive() + "\n"
}

// ── the counting test upstream ───────────────────────────────────────────────

var (
	ansibleStubOnce sync.Once
	ansibleStubVal  *stubControl
)

// ansibleStubHost is the host of the Galaxy test upstream as deploy/dev/e2e.sh exports it.
func ansibleStubHost(t testing.TB) string {
	t.Helper()
	host := os.Getenv("TROVE_E2E_ANSIBLESTUB_HOST")
	if host == "" {
		t.Fatal("TROVE_E2E_ANSIBLESTUB_HOST is not set; run `deploy/dev/e2e.sh ansible`")
	}
	return host
}

// ansibleStub is the control client of the counting Galaxy test upstream; reset and resetAll work on it as on the other stubs (the control
// API has the same shape), ansibleMode and ansibleCounts are the Galaxy's.
func ansibleStub(t testing.TB) *stubControl {
	t.Helper()
	host := ansibleStubHost(t)
	ansibleStubOnce.Do(func() {
		ansibleStubVal = newStubControl(t, "svc/ansiblestub", host, "trove-ansiblestub", 30*time.Second)
	})
	return ansibleStubVal
}

// ansibleMode replaces the switches of the test upstream; the zero Mode clears them.
func (s *stubControl) ansibleMode(t testing.TB, m ansiblestub.Mode) {
	t.Helper()
	s.send(t, http.MethodPost, "/-/stub/mode", m)
}

// ansibleCounts reads the counters of the test upstream.
func (s *stubControl) ansibleCounts(t testing.TB) ansiblestub.Counts {
	t.Helper()
	var c ansiblestub.Counts
	if err := json.Unmarshal(s.send(t, http.MethodGet, "/-/stub/counts", nil), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// ── the setup snippets ───────────────────────────────────────────────────────

// setupSnippets are the snippets of the client setup of a repository by id (credentials=env: the token is the reference $TROVE_TOKEN).
func (fx *ansibleFixture) setupSnippets(t testing.TB, key, pkg, version string) map[string]string {
	t.Helper()
	q := "client=ansible-galaxy&credentials=env&package=" + pkg + "&version=" + version
	r := fx.adm.get(repoAPI(key)+"/client-setup?"+q).want(t, "client setup of "+key+" for ansible-galaxy", 200)
	var setup struct {
		Snippets []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"snippets"`
	}
	r.JSON(t, &setup)
	out := map[string]string{}
	for _, s := range setup.Snippets {
		out[s.ID] = s.Content
	}
	return out
}
