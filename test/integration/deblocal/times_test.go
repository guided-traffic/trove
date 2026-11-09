//go:build integration

package deblocal

import (
	"bytes"
	"crypto"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/guided-traffic/trove/internal/openpgp"
)

func (e *env) keysPath() string {
	return "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys"
}

// dateSample is a Release Date read from the published InRelease and the database time right after the read.
type dateSample struct {
	date, dbNow time.Time
}

// sampleDates reads InRelease of the suite every 100 ms until stop is closed and returns what it saw.
func (e *env) sampleDates(suite string, stop <-chan struct{}) []dateSample {
	var out []dateSample
	for {
		select {
		case <-stop:
			return out
		case <-time.After(100 * time.Millisecond):
		}
		a := e.get("dists/"+suite+"/Release", e.tokA)
		if a.Status != 200 {
			continue
		}
		ps := paragraphs(e.t, a.Body)
		if len(ps) != 1 {
			continue
		}
		out = append(out, dateSample{date: releaseDate(e.t, ps[0]), dbNow: e.dbNow()})
	}
}

// R-DEB-010, Q-DEB-30: twenty key changes in five seconds, each of which enqueues a build at once, publish Dates that never run ahead of
// the database clock by more than the rule allows (never later than its time minus 59 s) and never go backwards.
func TestR_DEB_010_TwentyKeyChangesInFiveSecondsNeverDateAhead(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var samples []dateSample
	wg.Add(1)
	go func() { defer wg.Done(); samples = e.sampleDates("stable", stop) }()

	start := time.Now()
	for i := 0; i < 10; i++ {
		c := e.st.Do(e.r, "POST", e.keysPath(), e.tokAdmin, map[string]any{"generate": true})
		if c.Status/100 != 2 {
			t.Fatalf("create next key %d: %d %s", i, c.Status, c.Body)
		}
		var key struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(c.Body, &key); err != nil || key.Fingerprint == "" {
			t.Fatalf("answer %s: %v", c.Body, err)
		}
		if d := e.st.Do(e.r, "DELETE", e.keysPath()+"/"+key.Fingerprint, e.tokAdmin, nil); d.Status/100 != 2 {
			t.Fatalf("retire next key %d: %d %s", i, d.Status, d.Body)
		}
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Logf("the twenty changes took %s, longer than five seconds on this host", took)
	}
	// The builds follow; let the last one publish.
	time.Sleep(4 * time.Second)
	seq := e.st.Number(`SELECT s.change_seq FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = 'stable'`, repo)
	e.waitIndex("stable", seq)
	close(stop)
	wg.Wait()

	if len(samples) < 5 {
		t.Fatalf("only %d samples of the Release", len(samples))
	}
	distinct := 0
	for i, s := range samples {
		if s.date.After(s.dbNow.Add(-59 * time.Second)) {
			t.Errorf("a Release dated %s was served at the database time %s: later than the time minus 59 s", s.date, s.dbNow)
		}
		if i > 0 {
			if s.date.Before(samples[i-1].date) {
				t.Errorf("the Date went backwards from %s to %s", samples[i-1].date, s.date)
			}
			if s.date.After(samples[i-1].date) {
				distinct++
			}
		}
	}
	if distinct < 1 {
		t.Error("no build published a new generation during the key changes")
	}
	rel, _ := e.release("stable")
	if got := releaseDate(t, rel); !got.Before(e.dbNow().Add(-30 * time.Second)) {
		t.Errorf("the final Date %s is less than 30 s behind the database", got)
	}
	t.Logf("%d samples, %d distinct Dates", len(samples), distinct+1)
}

// importable is an EdDSA key created a day ago whose only self-signature was made at bound.
func importable(t *testing.T, created, bound time.Time) []byte {
	t.Helper()
	cfg := &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519, DefaultHash: crypto.SHA512, Time: func() time.Time { return created }}
	ent, err := pgp.NewEntity("imported", "", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ent.Identities {
		sig := *id.SelfSignature
		sig.CreationTime = bound
		if err := sig.SignUserId(id.Name, ent.PrimaryKey, ent.PrivateKey, &packet.Config{DefaultHash: id.SelfSignature.Hash, Time: func() time.Time { return bound }}); err != nil {
			t.Fatal(err)
		}
		id.SelfSignature = &sig
		id.Signatures = []*packet.Signature{&sig}
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, pgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ent.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return append(buf.Bytes(), '\n')
}

// R-DEB-016, Q-DEB-40: an imported key whose only self-signature is younger than its creation time by a day signs only after that
// self-signature: the build waits, no generation published before then carries its signature, and the Date of the one that does is
// later than the self-signature and more than 30 s behind the database.
func TestR_DEB_016_AFreshlyBoundImportedKeySignsOnlyAfterItsSelfSignature(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	first := e.waitIndex("stable", seqOf(e, a, "stable"))

	now := e.dbNow()
	bound := now.Add(-20 * time.Second)
	secret := importable(t, now.Add(-24*time.Hour), bound)
	c := e.st.Do(e.r, "POST", e.keysPath(), e.tokAdmin, map[string]any{"privateKey": string(secret)})
	if c.Status/100 != 2 {
		t.Fatalf("import: %d %s", c.Status, c.Body)
	}
	var key struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal(c.Body, &key); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var samples []dateSample
	wg.Add(1)
	go func() { defer wg.Done(); samples = e.sampleDates("stable", stop) }()
	deadline := time.Now().Add(150 * time.Second)
	var st suiteState
	for time.Now().Before(deadline) {
		st = e.waitIndex("stable", 1)
		if len(st.SignedBy) == 2 && st.Generation > first.Generation {
			break
		}
		time.Sleep(time.Second)
	}
	close(stop)
	wg.Wait()
	if len(st.SignedBy) != 2 {
		t.Fatalf("the imported key did not sign within the time: %+v", st)
	}

	rel, _ := e.release("stable")
	date := releaseDate(t, rel)
	if !date.After(bound) {
		t.Errorf("the Date %s is not later than the key's self-signature %s", date, bound)
	}
	if !date.Before(e.dbNow().Add(-30 * time.Second)) {
		t.Errorf("the Date %s is less than 30 s behind the database", date)
	}
	for _, s := range samples {
		if s.date.Before(bound.Add(time.Second)) && s.date.After(first.parsedDate(t)) {
			t.Errorf("a generation dated %s was published before the key may sign (%s)", s.date, bound.Add(time.Second))
		}
	}
	// The imported key alone verifies both documents.
	var imported []openpgp.Certificate
	for _, c := range e.certs() {
		if c.Fingerprint == key.Fingerprint {
			imported = append(imported, c)
		}
	}
	if len(imported) != 1 {
		t.Fatalf("signing-key.asc holds %d certificates of the imported key", len(imported))
	}
	in, gpg, relDoc := e.get("dists/stable/InRelease", e.tokA), e.get("dists/stable/Release.gpg", e.tokA), e.get("dists/stable/Release", e.tokA)
	if _, _, err := openpgp.VerifyClear(in.Body, imported, e.dbNow(), openpgp.DefaultLimits()); err != nil {
		t.Errorf("InRelease with the imported key alone: %v", err)
	}
	if _, err := openpgp.VerifyDetached(relDoc.Body, gpg.Body, imported, e.dbNow(), openpgp.DefaultLimits()); err != nil {
		t.Errorf("Release.gpg with the imported key alone: %v", err)
	}
}

func (s suiteState) parsedDate(t *testing.T) time.Time {
	t.Helper()
	d, err := time.Parse(time.RFC3339, s.ReleaseDate)
	if err != nil {
		return time.Time{}
	}
	return d
}
