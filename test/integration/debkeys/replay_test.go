//go:build integration

package debkeys

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/journal"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// replay applies one entry to the restored database the way the replayer does: in a transaction of its own, through the handler the
// domain registered.
func (e *env) replay(event string, entry map[string]any) journal.Result {
	e.t.Helper()
	h, ok := journal.HandlerFor(event)
	if !ok {
		e.t.Fatalf("no replay handler for %s", event)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		e.t.Fatal(err)
	}
	r := testenv.RandHex(16)
	id := r[:8] + "-" + r[8:12] + "-4" + r[13:16] + "-8" + r[17:20] + "-" + r[20:]
	at := time.Now().Add(-time.Minute)
	o := journal.Object{V: journal.Version, ID: id, At: at.UTC(), Event: event, Entry: raw}
	if _, err := o.Encode(); err != nil {
		e.t.Fatalf("object: %v", err)
	}
	var res journal.Result
	err = e.db.InTx(context.Background(), func(tx meta.Tx) error {
		var err error
		res, err = h.Apply(context.Background(), tx, journal.Env{Now: time.Now(), RestoredTo: at.Add(-time.Hour)}, o)
		return err
	})
	if err != nil {
		e.t.Fatalf("replay %s: %v", event, err)
	}
	return res
}

func promoted(repoID int64, old, next string) map[string]any {
	return map[string]any{"repositoryId": repoID, "oldFingerprint": old, "newFingerprint": next, "actorId": ""}
}

func retired(repoID int64, fingerprint string) map[string]any {
	return map[string]any{"repositoryId": repoID, "fingerprint": fingerprint, "actorId": ""}
}

// A key created before the snapshot and promoted after it: the restored repository holds A as primary and B as next.
func TestR_DEB_032_ReplayOfAPromotionWithTheNewKeyPresent(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-replay")
	e.st.Exec(`INSERT INTO deb_suites (repo_id, suite) VALUES ($1, 'stable')`, id)
	a := fp(byState(e.keys("apt-replay"), "primary"))
	next := e.call("admin", "POST", keysPath("apt-replay"), map[string]any{"generate": true}).JSON(t)
	b := fp(next)
	sealedA := e.st.Text(`SELECT private_key FROM deb_signing_keys WHERE fingerprint = $1`, a)
	seq := e.changeSeq(id, "stable")

	res := e.replay(audit.EventDebSigningKeyPromoted, promoted(id, a, b))
	if res.Outcome != journal.Applied {
		t.Fatalf("outcome %s (%s)", res.Outcome, res.Subject)
	}
	if st, private := e.dbState(b); st != "primary" || !private {
		t.Errorf("the new key is %s, private %v", st, private)
	}
	if st, private := e.dbState(a); st != "withheld" || !private {
		t.Errorf("the old key is %s, private %v: the replay withholds and keeps the sealed key", st, private)
	}
	if got := e.st.Text(`SELECT private_key FROM deb_signing_keys WHERE fingerprint = $1`, a); got != sealedA {
		t.Error("the replay changed the sealed private key")
	}
	if e.changeSeq(id, "stable") <= seq {
		t.Error("change_seq was not raised, the next build would not re-sign")
	}
	if res.Note != "" {
		t.Errorf("a repository with a primary got the note %q", res.Note)
	}
	// A withheld key neither signs nor is served.
	signers, err := e.store.SigningKeys(context.Background(), id)
	if err != nil || len(signers) != 1 || signers[0].Fingerprint != b {
		t.Errorf("signing keys %v, %v", signers, err)
	}
	served, _, _ := e.store.PublicCertificates(context.Background(), id)
	if c, err := openpgp.ParseCertificates(served, time.Now(), openpgp.Limits{}); err != nil || len(c) != 1 || c[0].Fingerprint != b {
		t.Errorf("signing-key.asc: %v, %v", c, err)
	}
	if k := byState(e.keys("apt-replay"), "withheld"); k == nil || fp(k) != a || k["withheldAt"] == nil {
		t.Errorf("the list does not show the withheld key: %v", e.keys("apt-replay"))
	}
	if r := e.replay(audit.EventDebSigningKeyPromoted, promoted(id, a, b)); r.Outcome != journal.AlreadyInEffect {
		t.Errorf("a second replay: %s", r.Outcome)
	}

	// The administrator reinstates the withheld key: the repository has a primary, so it returns as next.
	back := e.call("maintainer", "POST", keysPath("apt-replay", "/", a, "/reinstate"), nil)
	if back.Status != 200 || back.JSON(t)["state"] != "next" || back.JSON(t)["fingerprint"] != a {
		t.Fatalf("reinstate: %d %s", back.Status, back.Body)
	}
	if e.eventCount("deb.signing_key.reinstated", "apt-replay") != 1 {
		t.Error("the reinstatement is not audited")
	}
	if again := e.call("maintainer", "POST", keysPath("apt-replay", "/", a, "/reinstate"), nil); again.Status != 409 || again.Code(t) != "deb_key_state" {
		t.Errorf("reinstating a key that is not withheld: %d %s", again.Status, again.Body)
	}

	// Or confirms the retirement: the private key is erased and deb.signing_key.retired is written.
	if x := e.call("maintainer", "DELETE", keysPath("apt-replay", "/", a), nil); x.Status != 204 {
		t.Fatalf("retire the reinstated next key: %d", x.Status)
	}
	// A withheld key again, then confirmed.
	e.replay(audit.EventDebSigningKeyRetired, retired(id, b))
	if st, _ := e.dbState(b); st != "withheld" {
		t.Fatalf("state %s", st)
	}
	if x := e.call("maintainer", "DELETE", keysPath("apt-replay", "/", b), nil); x.Status != 204 {
		t.Fatalf("confirm: %d %s", x.Status, x.Body)
	}
	if st, private := e.dbState(b); st != "retired" || private {
		t.Errorf("after the confirmation the key is %s, private %v", st, private)
	}
	if e.eventCount("deb.signing_key.retired", "apt-replay") != 2 {
		t.Errorf("%d retirement events", e.eventCount("deb.signing_key.retired", "apt-replay"))
	}
}

// A key created and promoted after the snapshot: the restored repository does not hold the new key.
func TestR_DEB_032_ReplayOfAPromotionWithoutTheNewKey(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-lost")
	a := fp(byState(e.keys("apt-lost"), "primary"))
	unknown := strings.Repeat("AB", 20)
	res := e.replay(audit.EventDebSigningKeyPromoted, promoted(id, a, unknown))
	if res.Outcome != journal.Applied {
		t.Fatalf("outcome %s", res.Outcome)
	}
	if !strings.Contains(res.Note, "no primary") || !strings.Contains(res.Note, project+"/apt-lost") {
		t.Errorf("the replay output does not name the repository without a primary: %q", res.Note)
	}
	if st, private := e.dbState(a); st != "withheld" || !private {
		t.Errorf("the old key is %s, private %v", st, private)
	}
	// The repository has no primary: the build's failure no_signing_key.
	if signers, err := e.store.SigningKeys(context.Background(), id); err != nil || len(signers) != 0 {
		t.Errorf("signing keys %v, %v", signers, err)
	}
	if served, _, _ := e.store.PublicCertificates(context.Background(), id); len(served) != 0 {
		t.Error("signing-key.asc still serves the withheld key")
	}
	if r := e.replay(audit.EventDebSigningKeyPromoted, promoted(id, a, unknown)); r.Outcome != journal.AlreadyInEffect {
		t.Errorf("a second replay: %s", r.Outcome)
	}
	// The administrator reinstates the old key: it is the primary again.
	back := e.call("admin", "POST", keysPath("apt-lost", "/", a, "/reinstate"), nil)
	if back.Status != 200 || back.JSON(t)["state"] != "primary" {
		t.Fatalf("reinstate: %d %s", back.Status, back.Body)
	}
	if signers, err := e.store.SigningKeys(context.Background(), id); err != nil || len(signers) != 1 {
		t.Errorf("signing keys after the reinstatement: %v, %v", signers, err)
	}
	// Or the repository gets a new key in place of the lost one: a next key, promoted without an old primary.
	e.replay(audit.EventDebSigningKeyRetired, retired(id, a))
	if x := e.call("admin", "DELETE", keysPath("apt-lost", "/", a), nil); x.Status != 204 {
		t.Fatalf("confirm: %d %s", x.Status, x.Body)
	}
	fresh := e.call("admin", "POST", keysPath("apt-lost"), map[string]any{"generate": true}).JSON(t)
	if x := e.call("admin", "POST", keysPath("apt-lost", "/promote"), nil); x.Status != 200 || x.JSON(t)["fingerprint"] != fp(fresh) {
		t.Fatalf("promote without a primary: %d %s", x.Status, x.Body)
	}
	entry := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'deb.signing_key.promoted' AND entry::text LIKE '%' || $1 || '%'`, fp(fresh))
	if strings.Contains(entry, "oldFingerprint") {
		t.Errorf("a promotion with no old primary names one: %s", entry)
	}
}

func TestR_DEB_032_ReplayOfARetirement(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-retired")
	a := fp(byState(e.keys("apt-retired"), "primary"))
	b := fp(e.call("admin", "POST", keysPath("apt-retired"), map[string]any{"generate": true}).JSON(t))
	other := e.localRepo("apt-retired-other")
	foreign := fp(byState(e.keys("apt-retired-other"), "primary"))

	if r := e.replay(audit.EventDebSigningKeyRetired, retired(id, b)); r.Outcome != journal.Applied {
		t.Errorf("the next key: %s", r.Outcome)
	}
	if st, private := e.dbState(b); st != "withheld" || !private {
		t.Errorf("the key is %s, private %v", st, private)
	}
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(id, b)); r.Outcome != journal.AlreadyInEffect {
		t.Errorf("again: %s", r.Outcome)
	}
	// A key another repository holds, an unknown key and a repository that is no Debian repository are subject unknown.
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(id, foreign)); r.Outcome != journal.SubjectUnknown {
		t.Errorf("a key of another repository: %s", r.Outcome)
	}
	if st, _ := e.dbState(foreign); st != "primary" {
		t.Errorf("the other repository's key is %s", st)
	}
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(id, strings.Repeat("CD", 20))); r.Outcome != journal.SubjectUnknown {
		t.Errorf("an unknown key: %s", r.Outcome)
	}
	e.st.Repository("apt-remote", "remote", "deb", "{}")
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(e.repoID("apt-remote"), a)); r.Outcome != journal.SubjectUnknown {
		t.Errorf("a remote repository: %s", r.Outcome)
	}
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(999999999, a)); r.Outcome != journal.SubjectUnknown {
		t.Errorf("an unknown repository: %s", r.Outcome)
	}
	// A forged entry for the primary key costs availability, never the key: it is withheld with its sealed private key.
	if r := e.replay(audit.EventDebSigningKeyRetired, retired(id, a)); r.Outcome != journal.Applied || !strings.Contains(r.Note, "no primary") {
		t.Errorf("the primary key: %s, note %q", r.Outcome, r.Note)
	}
	if st, private := e.dbState(a); st != "withheld" || !private {
		t.Errorf("the primary key is %s, private %v", st, private)
	}
	// A promotion whose old key is not the repository's primary describes another repository: nothing changes.
	if st, _ := e.dbState(foreign); st != "primary" {
		t.Fatal("setup")
	}
	n := fp(e.call("admin", "POST", keysPath("apt-retired-other"), map[string]any{"generate": true}).JSON(t))
	if r := e.replay(audit.EventDebSigningKeyPromoted, promoted(other, strings.Repeat("EF", 20), n)); r.Outcome != journal.SubjectUnknown {
		t.Errorf("a promotion with a foreign old key: %s", r.Outcome)
	}
	if st, _ := e.dbState(n); st != "next" {
		t.Errorf("the next key is %s", st)
	}
	if st, _ := e.dbState(foreign); st != "primary" {
		t.Errorf("the primary key is %s", st)
	}
	// Nothing was erased by any replay: every key that is not retired still holds its private key.
	if c := e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE state <> 'retired' AND private_key IS NULL`); c != 0 {
		t.Errorf("%d keys lost their private key", c)
	}
}

func TestR_DEB_032_AMalformedKeyEntryIsSkipped(t *testing.T) {
	e := newEnv(t)
	for name, entry := range map[string]map[string]any{
		"no repository":  {"fingerprint": strings.Repeat("A", 40)},
		"no fingerprint": {"repositoryId": 1},
		"lower case":     {"repositoryId": 1, "fingerprint": strings.Repeat("a", 40)},
	} {
		if r := e.replay(audit.EventDebSigningKeyRetired, entry); r.Outcome != journal.Skipped {
			t.Errorf("%s: %s", name, r.Outcome)
		}
	}
}
