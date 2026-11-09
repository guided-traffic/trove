//go:build integration

package debremote

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/debstub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// reposPath is the management API's collection of the default project.
const reposPath = "/api/v1/projects/" + project + "/repositories"

// api sends a request to the management API as the administrator.
func (e *denv) api(method, path string, body any, hdr ...string) (status int, doc map[string]any, raw []byte) {
	e.t.Helper()
	a := e.st.Do(e.r, method, path, e.admin, body, hdr...)
	var m map[string]any
	_ = json.Unmarshal(a.Body, &m) //nolint:errcheck // an answer that is no object leaves the map empty
	return a.Status, m, a.Body
}

// createRemote creates a remote Debian repository through the service, so that the creation validates the keys and stores their
// fingerprints. ttl is the metadata period in seconds.
func (e *denv) createRemote(key, keys string, ttl int) {
	e.t.Helper()
	deb := map[string]any{}
	if keys != "" {
		deb["upstreamKeys"] = keys
	}
	st, _, raw := e.api(http.MethodPost, reposPath, map[string]any{
		"key": key, "class": "remote", "format": "deb",
		"remote": map[string]any{"url": e.srv.URL + "/debian/", "cache": map[string]any{"metadataTtlSeconds": ttl}},
		"deb":    deb,
	})
	if st != http.StatusCreated {
		e.t.Fatalf("create %s: %d %s", key, st, raw)
	}
}

// setKeys replaces deb.upstreamKeys of a repository.
func (e *denv) setKeys(key, keys string) (int, []byte) {
	e.t.Helper()
	st, _, raw := e.api(http.MethodPatch, reposPath+"/"+key, map[string]any{"deb": map[string]any{"upstreamKeys": keys}},
		"Content-Type", "application/merge-patch+json", "If-Match", "*")
	return st, raw
}

func (e *denv) fingerprint(name string) string {
	e.t.Helper()
	k, ok := e.stub.Key(name)
	if !ok {
		e.t.Fatalf("no key %s", name)
	}
	return k.Fingerprint
}

func (e *denv) verifiedBy(key string) string {
	e.t.Helper()
	return e.st.Text(`SELECT COALESCE(verified_by, '') FROM deb_remote_releases WHERE repo_id = $1 AND document = 'InRelease'`, e.repoID(key))
}

func TestR_DEB_020_TheKeysAreCheckedWhenTheyAreSavedAndTheirFingerprintsShown(t *testing.T) {
	e := newEnv(t)
	ring, err := e.stub.Keyring("a", "b")
	if err != nil {
		t.Fatal(err)
	}
	st, doc, raw := e.api(http.MethodPost, reposPath, map[string]any{
		"key": "keys-ok", "class": "remote", "format": "deb", "remote": map[string]any{"url": e.srv.URL + "/debian/"},
		"deb": map[string]any{"upstreamKeys": string(ring)},
	})
	if st != http.StatusCreated {
		t.Fatalf("%d %s", st, raw)
	}
	fps, _ := doc["deb"].(map[string]any)["upstreamKeyFingerprints"].([]any)
	if len(fps) != 2 || fps[0] != e.fingerprint("a") || fps[1] != e.fingerprint("b") {
		t.Errorf("fingerprints = %v", fps)
	}
	// A block that is no key, and a key that has expired, answer 422 with the reason at the keys' pointer.
	expired, err := e.stub.Keyring("d")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not a key", string(expired)} {
		st, _, raw := e.api(http.MethodPost, reposPath, map[string]any{
			"key": "keys-bad", "class": "remote", "format": "deb", "remote": map[string]any{"url": e.srv.URL + "/debian/"},
			"deb": map[string]any{"upstreamKeys": bad},
		})
		if st != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "/deb/upstreamKeys") {
			t.Errorf("%.20q: %d %s", bad, st, raw)
		}
	}
	// A remote without keys carries the warning of R-DEB-020.
	st, doc, raw = e.api(http.MethodPost, reposPath, map[string]any{
		"key": "keys-none", "class": "remote", "format": "deb", "remote": map[string]any{"url": e.srv.URL + "/debian/"},
	})
	if st != http.StatusCreated || !strings.Contains(string(raw), "deb_upstream_unverified") {
		t.Errorf("without keys: %d %s", st, raw)
	}
	_ = doc
}

func TestR_DEB_020_ARemoteThatIsAMirrorNeedsKeys(t *testing.T) {
	e := newEnv(t)
	st, _, raw := e.api(http.MethodPost, reposPath, map[string]any{
		"key": "mirror-nokeys", "class": "remote", "format": "deb",
		"remote": map[string]any{"url": e.srv.URL + "/debian/", "mode": "mirror"},
	})
	// The mirror mode of the Debian format is the mirror work package's: until it lands the mode is refused as not applicable, and
	// never accepted without keys.
	if st == http.StatusCreated {
		t.Fatalf("a mirror without keys was created: %s", raw)
	}
}

// Removing a key re-verifies the releases it verified from their stored bytes, with no request to the upstream, when another key of
// the new set verifies them (R-DEB-020, Q-DEB-42).
func TestR_DEB_020_AReleaseAnotherKeyAlsoVerifiesIsVerifiedAgainByTheJobWithoutARequest(t *testing.T) {
	e := newEnv(t)
	ring, _ := e.stub.Keyring("a", "b")
	if err := e.stub.SetMode(debstub.Mode{SignWith: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	e.createRemote("kc-cosigned", string(ring), 300)
	if a := e.get("kc-cosigned", suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	first := e.verifiedBy("kc-cosigned")
	if first == "" {
		t.Fatal("the release is not verified")
	}
	// Remove the key that verified it.
	remaining, other := "b", e.fingerprint("b")
	if first == e.fingerprint("b") {
		remaining, other = "a", e.fingerprint("a")
	}
	keep, _ := e.stub.Keyring(remaining)
	e.stub.Reset()
	if st, raw := e.setKeys("kc-cosigned", string(keep)); st != http.StatusOK {
		t.Fatalf("%d %s", st, raw)
	}
	eventually(t, 30*time.Second, "the job to verify the release again", func() bool { return e.verifiedBy("kc-cosigned") == other })
	if n := e.stub.Counts().Total(); n != 0 {
		t.Errorf("the re-verification sent the stub %d requests: %+v", n, e.stub.Counts().Entries)
	}
	// The removal is journaled as a part of repository.narrowed.
	if n := e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'repository.narrowed' AND entry::text LIKE '%debUpstreamKeysRemoved%' AND entry::text LIKE '%' || $1 || '%'`, first); n != 1 {
		t.Errorf("journal entries of the removal = %d", n)
	}
}

// A release the remaining keys do not verify stays unverified, nothing is served from its directory, and its next revalidation is sent
// without validators so that the upstream's body is verified (the upstream's 304 never marks a release verified).
func TestR_DEB_020_AReleaseNoKeyOfTheNewSetVerifiesIsFetchedAgainWithoutValidators(t *testing.T) {
	e := newEnv(t)
	ring, _ := e.stub.Keyring("a", "b")
	e.createRemote("kc-single", string(ring), 2)
	if a := e.get("kc-single", suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if e.verifiedBy("kc-single") != e.fingerprint("a") {
		t.Fatalf("verified by %s", e.verifiedBy("kc-single"))
	}
	keepB, _ := e.stub.Keyring("b")
	if st, raw := e.setKeys("kc-single", string(keepB)); st != http.StatusOK {
		t.Fatalf("%d %s", st, raw)
	}
	if v := e.verifiedBy("kc-single"); v != "" {
		t.Fatalf("verified_by = %s after the removal of the key that verified it", v)
	}
	// Nothing is served from the directory while its release is unverified.
	if a := e.get("kc-single", suitePath+packages+".gz"); a.Status == http.StatusOK {
		t.Errorf("an index was served from an unverified directory")
	}
	// The upstream now signs with the key b; the next revalidation, sent without validators, verifies the new body.
	if err := e.stub.SetMode(debstub.Mode{SignWith: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	e.stub.Reset()
	if a := e.get("kc-single", suitePath+"InRelease"); a.Status != http.StatusOK {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	c := e.stub.Counts()
	if c.Conditional("release") != 0 {
		t.Errorf("%d conditional requests for the release, want none: %+v", c.Conditional("release"), c.Entries)
	}
	if c.Delivered("release") != 1 {
		t.Errorf("%d deliveries of the release, want 1", c.Delivered("release"))
	}
	if v := e.verifiedBy("kc-single"); v != e.fingerprint("b") {
		t.Errorf("verified_by = %q, want the fingerprint of b", v)
	}
}

// The replay of the journaled removal takes the fingerprints out of the restored document again, and fails closed for a remote that
// would be left without keys while it is a mirror or a member of a virtual repository.
func TestR_DEB_032_TheReplayOfARemovalTakesTheFingerprintsOutOfTheRestoredDocument(t *testing.T) {
	e := newEnv(t)
	ring, _ := e.stub.Keyring("a", "b")
	e.createRemote("kc-replay", string(ring), 300)
	id := e.repoID("kc-replay")
	h, ok := journal.HandlerFor(audit.EventRepositoryNarrowed)
	if !ok {
		t.Fatal("no replay handler for repository.narrowed")
	}
	apply := func(removed ...string) journal.Result {
		entry, _ := json.Marshal(map[string]any{"repositoryId": strconv.FormatInt(id, 10), "key": project + "/kc-replay", "debUpstreamKeysRemoved": removed})
		o := journal.Object{V: journal.Version, ID: "11111111-2222-4333-8444-555555555555", At: time.Now().Add(-time.Minute).UTC(), Event: audit.EventRepositoryNarrowed, Entry: entry}
		if _, err := o.Encode(); err != nil {
			t.Fatal(err)
		}
		db, err := postgres.Open(context.Background(), e.st.DatabaseConfig(), "debremote", meta.RoleWeb, testenv.Quiet())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var res journal.Result
		if err := db.InTx(context.Background(), func(tx meta.Tx) error {
			var err error
			res, err = h.Apply(context.Background(), tx, journal.Env{Now: time.Now(), RestoredTo: time.Now().Add(-time.Hour)}, o)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := apply(e.fingerprint("a")); res.Outcome != journal.Applied {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	keys := e.st.Text(`SELECT config -> 'deb' ->> 'upstreamKeys' FROM repositories WHERE id = $1`, id)
	certs, err := openpgp.ParseStoredCertificates([]byte(keys), openpgp.Limits{})
	if err != nil || len(certs) != 1 || certs[0].Fingerprint != e.fingerprint("b") {
		t.Errorf("the document holds %d certificates after the replay: %v", len(certs), err)
	}
	// A second replay finds the document as it is.
	if res := apply(e.fingerprint("a")); res.Outcome != journal.AlreadyInEffect {
		t.Errorf("second replay = %s", res.Outcome)
	}
}
