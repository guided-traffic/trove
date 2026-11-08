//go:build integration

package debkeys

import (
	"bytes"
	"context"
	"crypto"
	"strings"
	"sync"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/guided-traffic/trove/internal/audit"
	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/internal/secrets"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestR_DEB_015_ACreatedRepositoryHasAGeneratedPrimaryKey(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-first")
	keys := e.keys("apt-first")
	if len(keys) != 1 {
		t.Fatalf("%d keys, want the one primary key: %v", len(keys), keys)
	}
	k := keys[0]
	if k["state"] != "primary" || k["origin"] != "generated" || k["algorithm"] != float64(22) || k["bits"] != float64(256) {
		t.Fatalf("key %v", k)
	}
	if k["keyCreatedAt"] != k["boundSince"] {
		t.Errorf("keyCreatedAt %v and boundSince %v differ: the self-signature carries the creation time (Q-DEB-40)", k["keyCreatedAt"], k["boundSince"])
	}
	// The key is created one hour before the creating transaction (Q-DEB-30).
	lead := e.st.Number(`SELECT extract(epoch FROM (created_at - key_created))::bigint FROM deb_signing_keys WHERE fingerprint = $1`, fp(k))
	if lead < 3600 || lead > 3601 {
		t.Errorf("the key was created %d s before its row, want 3600 s", lead)
	}
	// Its certificate is what trove's own library reads, with the user ID of the repository.
	certs, err := openpgp.ParseCertificates([]byte(k["publicKey"].(string)), time.Now(), openpgp.Limits{})
	if err != nil || len(certs) != 1 || certs[0].Fingerprint != fp(k) || certs[0].Algorithm != 22 {
		t.Fatalf("the published certificate: %v, %v", certs, err)
	}
	if !strings.Contains(userIDs(t, k["publicKey"].(string)), "trove "+project+"/apt-first") {
		t.Errorf("user ID %q does not name the repository", userIDs(t, k["publicKey"].(string)))
	}
	// The private key is sealed under the key ring, bound to its row (R-ARCH-021).
	sealed := e.st.Text(`SELECT private_key FROM deb_signing_keys WHERE repo_id = $1`, id)
	if !strings.HasPrefix(sealed, "v1:"+e.ring.ActiveKeyID()+":") {
		t.Errorf("sealed value %.20q does not start with v1:<active key id>:", sealed)
	}
	if strings.Contains(sealed, "PRIVATE KEY") {
		t.Error("the stored private key is not sealed")
	}
	// The creation is audited, with the creator.
	if e.eventCount("deb.signing_key.created", "apt-first") != 1 {
		t.Errorf("%d creation events", e.eventCount("deb.signing_key.created", "apt-first"))
	}
	// Two repositories never share a key.
	e.localRepo("apt-second")
	if fp(e.keys("apt-second")[0]) == fp(k) {
		t.Error("two repositories share a fingerprint")
	}
}

func userIDs(t *testing.T, armored string) string {
	t.Helper()
	ents, err := pgp.ReadArmoredKeyRing(strings.NewReader(armored))
	if err != nil || len(ents) != 1 {
		t.Fatalf("read: %v", err)
	}
	var ids []string
	for name := range ents[0].Identities {
		ids = append(ids, name)
	}
	return strings.Join(ids, "|")
}

func TestR_DEB_015_OnlyLocalAndVirtualDebianRepositoriesHaveKeys(t *testing.T) {
	e := newEnv(t)
	e.st.Repository("apt-remote", "remote", "deb", "{}")
	if a := e.createRepo("npm-local", "local", "npm"); a.Status != 201 {
		t.Fatalf("create npm: %d %s", a.Status, a.Body)
	}
	for _, repo := range []string{"apt-remote", "npm-local"} {
		a := e.call("admin", "GET", keysPath(repo), nil)
		if a.Status != 400 || a.Code(t) != "not_applicable" {
			t.Errorf("%s: %d %s", repo, a.Status, a.Body)
		}
		if a := e.call("admin", "POST", keysPath(repo), map[string]any{"generate": true}); a.Status != 400 {
			t.Errorf("%s create: %d %s", repo, a.Status, a.Body)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_signing_keys`); n != 0 {
		t.Errorf("%d keys exist for repositories that have none", n)
	}
	// A virtual repository composed by the store gets its key the same way (the class is created through the API once it is built).
	e.st.Repository("apt-virtual", "virtual", "deb", "{}")
	id := e.repoID("apt-virtual")
	err := e.db.InTx(context.Background(), func(tx meta.Tx) error {
		return e.store.CreateFirstKey(context.Background(), tx, id, project+"/apt-virtual")
	})
	if err != nil {
		t.Fatal(err)
	}
	virt, err := e.store.Keys(context.Background(), id)
	if err != nil || len(virt) != 1 || virt[0].State != "primary" || virt[0].Algorithm != 22 {
		t.Errorf("virtual keys: %v, %v", virt, err)
	}
	if a := e.call("admin", "GET", keysPath("no-such-repo"), nil); a.Status != 404 {
		t.Errorf("an unknown repository: %d", a.Status)
	}
}

func TestR_DEB_015_ImportRules(t *testing.T) {
	e := newEnv(t)
	e.localRepo("apt-import")
	now := time.Now()
	secret := func(t *testing.T, opts craft) string { return craftKey(t, now, opts) }
	refused := map[string]string{
		"RSA 2048":                       secret(t, craft{rsaBits: 2048}),
		"algorithm 27":                   secret(t, craft{alg27: true}),
		"an expiry":                      secret(t, craft{lifetime: 3600 * 24 * 365}),
		"created an hour in the future":  generated(t, now.Add(time.Hour)),
		"a signing subkey":               secret(t, craft{signingSubkey: true}),
		"not an armored key":             "this is not a key",
		"a public key block":             publicOnly(t),
		"a protected key, no passphrase": secret(t, craft{passphrase: "hunter2"}),
	}
	for name, armored := range refused {
		a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{"privateKey": armored})
		if a.Status != 422 || a.Code(t) != "deb_key_unsupported" {
			t.Errorf("%s: %d %s", name, a.Status, a.Body)
		}
		if strings.Contains(string(a.Body), "-----BEGIN PGP PRIVATE KEY") {
			t.Errorf("%s: the answer echoes key material", name)
		}
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE origin = 'imported'`); n != 0 {
		t.Errorf("%d imported keys after only refusals", n)
	}
	if a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{"privateKey": secret(t, craft{passphrase: "hunter2"}), "passphrase": "wrong"}); a.Status != 422 {
		t.Errorf("a wrong passphrase: %d %s", a.Status, a.Body)
	}
	if a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{}); a.Status != 400 {
		t.Errorf("neither: %d", a.Status)
	}

	// A protected RSA key of 3072 bits is accepted with its passphrase and becomes the next key; the passphrase is not stored.
	rsaKey := secret(t, craft{rsaBits: 3072, passphrase: "hunter2"})
	a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{"privateKey": rsaKey, "passphrase": "hunter2"})
	if a.Status != 201 {
		t.Fatalf("import RSA 3072: %d %s", a.Status, a.Body)
	}
	got := a.JSON(t)
	if got["state"] != "next" || got["origin"] != "imported" || got["algorithm"] != float64(1) || got["bits"] != float64(3072) {
		t.Errorf("imported key %v", got)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE public_key LIKE '%hunter2%' OR private_key LIKE '%hunter2%'`); n != 0 {
		t.Error("the passphrase was stored")
	}
	sealed := e.st.Text(`SELECT private_key FROM deb_signing_keys WHERE fingerprint = $1`, fp(got))
	plain, err := e.ring.Open(debdata.SigningKeys, secrets.Row(e.st.Text(`SELECT id::text FROM deb_signing_keys WHERE fingerprint = $1`, fp(got))), sealed)
	if err != nil || !bytes.Contains(plain, []byte("PRIVATE KEY BLOCK")) {
		t.Errorf("the sealed key does not open: %v", err)
	}
	if bytes.Contains(plain, []byte("ENCRYPTED")) || strings.Contains(string(plain), "hunter2") {
		t.Error("the stored secret is still protected by the passphrase")
	}

	// The same key cannot enter a second repository, nor come back after it was retired.
	e.localRepo("apt-other")
	if a := e.call("admin", "POST", keysPath("apt-other"), map[string]any{"privateKey": rsaKey, "passphrase": "hunter2"}); a.Status != 409 || a.Code(t) != "deb_key_in_use" {
		t.Errorf("into a second repository: %d %s", a.Status, a.Body)
	}
	if a := e.call("admin", "DELETE", keysPath("apt-import", "/", fp(got)), nil); a.Status != 204 {
		t.Fatalf("retire: %d %s", a.Status, a.Body)
	}
	if a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{"privateKey": rsaKey, "passphrase": "hunter2"}); a.Status != 409 || a.Code(t) != "deb_key_in_use" {
		t.Errorf("after the retirement: %d %s", a.Status, a.Body)
	}

	// An EdDSALegacy key made by trove's own library imports too, and a key created in the past with a fresh binding is accepted.
	if a := e.call("admin", "POST", keysPath("apt-import"), map[string]any{"privateKey": generated(t, now.Add(-24*time.Hour))}); a.Status != 201 {
		t.Errorf("import EdDSALegacy: %d %s", a.Status, a.Body)
	}
}

func TestR_DEB_017_Rotation(t *testing.T) {
	e := newEnv(t)
	id := e.localRepo("apt-rotate")
	e.st.Exec(`INSERT INTO deb_suites (repo_id, suite) VALUES ($1, 'stable'), ($1, 'testing')`, id)
	old := byState(e.keys("apt-rotate"), "primary")

	// Step 1: the next key. Every suite is marked changed, so the next build re-signs.
	a := e.call("maintainer", "POST", keysPath("apt-rotate"), map[string]any{"generate": true})
	if a.Status != 201 {
		t.Fatalf("create next: %d %s", a.Status, a.Body)
	}
	next := a.JSON(t)
	if next["state"] != "next" || next["origin"] != "generated" || next["fingerprint"] == fp(old) {
		t.Fatalf("next key %v", next)
	}
	if e.changeSeq(id, "stable") != 2 || e.changeSeq(id, "testing") != 2 {
		t.Errorf("change_seq %d and %d, want both raised to 2", e.changeSeq(id, "stable"), e.changeSeq(id, "testing"))
	}
	if a := e.call("maintainer", "POST", keysPath("apt-rotate"), map[string]any{"generate": true}); a.Status != 409 || a.Code(t) != "deb_key_state" {
		t.Errorf("a second next key: %d %s", a.Status, a.Body)
	}

	// While the next key exists both sign, and signing-key.asc serves both certificates (the signing path of the build).
	signers, err := e.store.SigningKeys(context.Background(), id)
	if err != nil || len(signers) != 2 || signers[0].Fingerprint != fp(old) || signers[1].Fingerprint != fp(next) {
		t.Fatalf("signing keys %v, %v", signers, err)
	}
	var opened []*openpgp.Key
	for _, s := range signers {
		k, err := openpgp.ParseSecret(s.Secret, nil, time.Now(), openpgp.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		opened = append(opened, k)
	}
	armored, sum, err := e.store.PublicCertificates(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	certs, err := openpgp.ParseCertificates(armored, time.Now(), openpgp.Limits{})
	if err != nil || len(certs) != 2 || sum == [32]byte{} {
		t.Fatalf("certificates %v, %v", certs, err)
	}
	release := []byte("Origin: test\nSuite: stable\n")
	at := time.Now().Add(-time.Minute)
	signed, err := openpgp.ClearSign(release, opened, at)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range certs {
		if _, signer, err := openpgp.VerifyClear(signed, []openpgp.Certificate{c}, time.Now(), openpgp.Limits{}); err != nil || signer != c.Fingerprint {
			t.Errorf("the InRelease does not verify with key %d alone: %v", i, err)
		}
	}

	// Step 3: the promotion.
	for _, who := range []string{"reader", "developer"} {
		if a := e.call(who, "POST", keysPath("apt-rotate", "/promote"), nil); a.Status != 403 {
			t.Errorf("%s promotes: %d", who, a.Status)
		}
	}
	a = e.call("maintainer", "POST", keysPath("apt-rotate", "/promote"), nil)
	if a.Status != 200 || a.JSON(t)["fingerprint"] != fp(next) || a.JSON(t)["state"] != "primary" {
		t.Fatalf("promote: %d %s", a.Status, a.Body)
	}
	if st, private := e.dbState(fp(old)); st != "retired" || private {
		t.Errorf("the old key is %s with a private key: %v", st, private)
	}
	if e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE fingerprint = $1 AND retired_at IS NOT NULL AND retired_by = $2::uuid`, fp(old), e.who["maintainer"].ID) != 1 {
		t.Error("the retirement does not name the maintainer and the time")
	}
	if e.changeSeq(id, "stable") != 3 {
		t.Errorf("change_seq after the promotion %d, want 3", e.changeSeq(id, "stable"))
	}
	signers, err = e.store.SigningKeys(context.Background(), id)
	if err != nil || len(signers) != 1 || signers[0].Fingerprint != fp(next) {
		t.Errorf("signing keys after the promotion: %v, %v", signers, err)
	}
	after, _, _ := e.store.PublicCertificates(context.Background(), id)
	if c, err := openpgp.ParseCertificates(after, time.Now(), openpgp.Limits{}); err != nil || len(c) != 1 || c[0].Fingerprint != fp(next) {
		t.Errorf("signing-key.asc after the promotion: %v, %v", c, err)
	}
	// The journal entry names both fingerprints and nothing secret.
	entry := e.st.Text(`SELECT entry::text FROM revocation_outbox WHERE event = 'deb.signing_key.promoted' ORDER BY at DESC LIMIT 1`)
	for _, want := range []string{fp(old), fp(next), `"repositoryId"`} {
		if !strings.Contains(entry, want) {
			t.Errorf("journal entry %s lacks %s", entry, want)
		}
	}
	if strings.Contains(entry, "PRIVATE") {
		t.Error("the journal entry holds key material")
	}
	if e.eventCount("deb.signing_key.promoted", "apt-rotate") != 1 {
		t.Error("the promotion is not audited")
	}

	// A promotion without a next key, and the primary key cannot be retired directly.
	if a := e.call("maintainer", "POST", keysPath("apt-rotate", "/promote"), nil); a.Status != 409 || a.Code(t) != "deb_no_next_key" {
		t.Errorf("promote without a next key: %d %s", a.Status, a.Body)
	}
	if a := e.call("maintainer", "DELETE", keysPath("apt-rotate", "/", fp(next)), nil); a.Status != 409 || a.Code(t) != "deb_key_state" {
		t.Errorf("retire the primary: %d %s", a.Status, a.Body)
	}
	if a := e.call("maintainer", "DELETE", keysPath("apt-rotate", "/", fp(old)), nil); a.Status != 409 || a.Code(t) != "deb_key_state" {
		t.Errorf("retire a retired key: %d %s", a.Status, a.Body)
	}

	// Retiring the next key before a promotion ends the overlap the same way, with a journaled event.
	n2 := e.call("maintainer", "POST", keysPath("apt-rotate"), map[string]any{"generate": true}).JSON(t)
	if a := e.call("developer", "DELETE", keysPath("apt-rotate", "/", fp(n2)), nil); a.Status != 403 {
		t.Errorf("a developer retires: %d", a.Status)
	}
	if a := e.call("maintainer", "DELETE", keysPath("apt-rotate", "/", fp(n2)), nil); a.Status != 204 {
		t.Fatalf("retire next: %d %s", a.Status, a.Body)
	}
	if st, private := e.dbState(fp(n2)); st != "retired" || private {
		t.Errorf("the retired next key is %s with a private key: %v", st, private)
	}
	if e.st.Number(`SELECT count(*) FROM revocation_outbox WHERE event = 'deb.signing_key.retired' AND entry::text LIKE '%' || $1 || '%'`, fp(n2)) != 1 {
		t.Error("the retirement of the next key is not journaled")
	}
	if a := e.call("maintainer", "DELETE", keysPath("apt-rotate", "/", strings.Repeat("0", 40)), nil); a.Status != 404 {
		t.Errorf("an unknown fingerprint: %d", a.Status)
	}
	if a := e.call("maintainer", "DELETE", keysPath("apt-rotate", "/", "not-a-fingerprint"), nil); a.Status != 404 {
		t.Errorf("a malformed fingerprint: %d", a.Status)
	}
	// The listing keeps the history: the retired keys with their certificates and without any private part.
	list := e.keys("apt-rotate")
	if len(list) != 3 {
		t.Errorf("%d keys listed, want the primary and two retired", len(list))
	}
	for _, k := range list {
		if k["publicKey"] == "" {
			t.Errorf("key %v lost its certificate", k["fingerprint"])
		}
	}
}

func TestR_DEB_015_ReadAndManagePermissions(t *testing.T) {
	e := newEnv(t)
	e.localRepo("apt-perm")
	before := e.keys("apt-perm")
	for _, who := range []string{"reader", "developer", "maintainer", "admin"} {
		if a := e.call(who, "GET", keysPath("apt-perm"), nil); a.Status != 200 {
			t.Errorf("%s lists: %d %s", who, a.Status, a.Body)
		}
	}
	for _, who := range []string{"reader", "developer"} {
		for name, a := range map[string]testenv.Answer{
			"create":    e.call(who, "POST", keysPath("apt-perm"), map[string]any{"generate": true}),
			"promote":   e.call(who, "POST", keysPath("apt-perm", "/promote"), nil),
			"retire":    e.call(who, "DELETE", keysPath("apt-perm", "/", fp(before[0])), nil),
			"reinstate": e.call(who, "POST", keysPath("apt-perm", "/", fp(before[0]), "/reinstate"), nil),
		} {
			if a.Status != 403 {
				t.Errorf("%s %s: %d %s", who, name, a.Status, a.Body)
			}
		}
	}
	if a := e.call("stranger", "GET", keysPath("apt-perm"), nil); a.Status != 403 && a.Status != 404 {
		t.Errorf("a stranger lists: %d", a.Status)
	}
	if a := e.call("stranger", "POST", keysPath("apt-perm"), map[string]any{"generate": true}); a.Status != 403 && a.Status != 404 {
		t.Errorf("a stranger creates: %d", a.Status)
	}
	if got := e.keys("apt-perm"); len(got) != len(before) {
		t.Errorf("a refused request changed the keys: %v", got)
	}
	// No answer and no log line of any of this holds a private key.
	if logs := e.r.Logs.String(); strings.Contains(logs, "-----BEGIN PGP PRIVATE KEY") {
		t.Error("the replica's log holds a private key block")
	}
}

func TestR_DEB_015_ConcurrentChangesLeaveOneNextKey(t *testing.T) {
	e := newEnv(t)
	e.localRepo("apt-race")
	var wg sync.WaitGroup
	status := make([]int, 6)
	for i := range status {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status[i] = e.call("admin", "POST", keysPath("apt-race"), map[string]any{"generate": true}).Status
		}()
	}
	wg.Wait()
	created, conflicts := 0, 0
	for _, s := range status {
		switch s {
		case 201:
			created++
		case 409:
			conflicts++
		}
	}
	if created != 1 || conflicts != len(status)-1 {
		t.Errorf("statuses %v: want one 201 and the rest 409", status)
	}
	// Two promotions at once: one wins.
	status = make([]int, 4)
	for i := range status {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status[i] = e.call("admin", "POST", keysPath("apt-race", "/promote"), nil).Status
		}()
	}
	wg.Wait()
	ok := 0
	for _, s := range status {
		if s == 200 {
			ok++
		} else if s != 409 {
			t.Errorf("a promotion answered %d", s)
		}
	}
	if ok != 1 {
		t.Errorf("statuses %v: want exactly one promotion", status)
	}
	if n := e.st.Number(`SELECT count(*) FROM deb_signing_keys WHERE repo_id = $1 AND state = 'primary'`, e.repoID("apt-race")); n != 1 {
		t.Errorf("%d primary keys", n)
	}
}

func TestR_ARCH_021_APrivateKeyIsBoundToItsRow(t *testing.T) {
	e := newEnv(t)
	e.localRepo("apt-seal-a")
	e.localRepo("apt-seal-b")
	rowA := e.st.Text(`SELECT k.id::text FROM deb_signing_keys k JOIN repositories r ON r.id = k.repo_id WHERE r.key = 'apt-seal-a'`)
	sealedA := e.st.Text(`SELECT private_key FROM deb_signing_keys WHERE id = $1::uuid`, rowA)
	rowB := e.st.Text(`SELECT k.id::text FROM deb_signing_keys k JOIN repositories r ON r.id = k.repo_id WHERE r.key = 'apt-seal-b'`)
	if _, err := e.ring.Open(debdata.SigningKeys, secrets.Row(rowA), sealedA); err != nil {
		t.Fatalf("a key does not open under its own row: %v", err)
	}
	if _, err := e.ring.Open(debdata.SigningKeys, secrets.Row(rowB), sealedA); err == nil {
		t.Error("a sealed key opened under another row: the value could be moved between repositories")
	}
	// The store hands the opened secret to the build alone, and it signs.
	signers, err := e.store.SigningKeys(context.Background(), e.repoID("apt-seal-a"))
	if err != nil || len(signers) != 1 || !bytes.Contains(signers[0].Secret, []byte("PRIVATE KEY BLOCK")) {
		t.Fatalf("signing keys: %v", err)
	}
}

// craft describes a secret key the tests build with the module directly to hit a rule of R-DEB-015 that trove's own generator never
// breaks.
type craft struct {
	rsaBits       int
	alg27         bool
	lifetime      uint32
	signingSubkey bool
	passphrase    string
}

func craftKey(t *testing.T, now time.Time, c craft) string {
	t.Helper()
	created := now.Add(-48 * time.Hour).Truncate(time.Second)
	cfg := &packet.Config{
		Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519, DefaultHash: crypto.SHA512, KeyLifetimeSecs: c.lifetime,
		Time: func() time.Time { return created },
	}
	switch {
	case c.rsaBits > 0:
		cfg.Algorithm, cfg.RSABits = packet.PubKeyAlgoRSA, c.rsaBits
	case c.alg27:
		cfg.Algorithm = packet.PubKeyAlgoEd25519
	}
	e, err := pgp.NewEntity("crafted", "", "", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c.signingSubkey {
		if err := e.AddSigningSubkey(&packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Time: func() time.Time { return created }}); err != nil {
			t.Fatal(err)
		}
	}
	if c.passphrase != "" {
		if err := e.EncryptPrivateKeys([]byte(c.passphrase), nil); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, pgp.PrivateKeyType, nil)
	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return buf.String() + "\n"
}

// generated is a key made by trove's library, created at the time.
func generated(t *testing.T, created time.Time) string {
	t.Helper()
	secret, _, _, err := openpgp.Generate("trove test", created)
	if err != nil {
		t.Fatal(err)
	}
	return string(secret)
}

func publicOnly(t *testing.T) string {
	t.Helper()
	_, pub, _, err := openpgp.Generate("trove test", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return string(pub)
}

// signing-key.asc serves the primary and, while one exists, the next key as one block, and the clients fetch it with the repository's
// read permission (R-DEB-018).
func TestR_DEB_018_SigningKeyAsc(t *testing.T) {
	e := newEnv(t)
	e.localRepo("apt-asc")
	path := "/api/deb/" + project + "/apt-asc/signing-key.asc"
	fetch := func(who string, hdr ...string) testenv.Answer {
		return e.st.Do(e.r, "GET", path, e.tok[who], nil, hdr...)
	}
	certs := func(a testenv.Answer) []openpgp.Certificate {
		t.Helper()
		c, err := openpgp.ParseCertificates(a.Body, time.Now(), openpgp.Limits{})
		if err != nil {
			t.Fatalf("the served block does not parse: %v\n%s", err, a.Body)
		}
		return c
	}

	primary := fp(byState(e.keys("apt-asc"), "primary"))
	a := fetch("reader")
	if a.Status != 200 || a.Header.Get("Content-Type") != "application/pgp-keys" || a.Header.Get("ETag") == "" {
		t.Fatalf("GET = %d %v %.80s", a.Status, a.Header, a.Body)
	}
	if c := certs(a); len(c) != 1 || c[0].Fingerprint != primary {
		t.Errorf("certificates %v, want the primary %s", c, primary)
	}
	etag := a.Header.Get("ETag")
	if b := fetch("reader", "If-None-Match", etag); b.Status != 304 {
		t.Errorf("If-None-Match = %d", b.Status)
	}
	if b := fetch("stranger"); b.Status != 403 && b.Status != 404 {
		t.Errorf("a stranger fetches the key: %d", b.Status)
	}

	// With a next key both certificates are served, and the ETag changes.
	next := fp(e.call("maintainer", "POST", keysPath("apt-asc"), map[string]any{"generate": true}).JSON(t))
	b := fetch("reader", "If-None-Match", etag)
	if b.Status != 200 || b.Header.Get("ETag") == etag {
		t.Fatalf("after the next key: %d, same ETag %v", b.Status, b.Header.Get("ETag") == etag)
	}
	if c := certs(b); len(c) != 2 || c[0].Fingerprint != primary || c[1].Fingerprint != next {
		t.Errorf("certificates %v, want the primary and the next key", c)
	}
	// After the promotion only the new key is served.
	e.call("maintainer", "POST", keysPath("apt-asc", "/promote"), nil)
	if c := certs(fetch("reader")); len(c) != 1 || c[0].Fingerprint != next {
		t.Errorf("after the promotion: %v", c)
	}
	// A repository whose keys are all withheld has nothing to serve.
	e.replay(audit.EventDebSigningKeyRetired, retired(e.repoID("apt-asc"), next))
	if c := fetch("reader"); c.Status != 404 {
		t.Errorf("without a key: %d", c.Status)
	}
}
