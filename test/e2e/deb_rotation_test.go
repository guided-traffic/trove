//go:build e2e

package e2e

// The rotation table of R-DEB-017 with the real clients: a keyring that holds only the old key, one that holds both and one that holds only
// the new key update from the same repository before the next key exists, while both keys sign, and after the promotion. Debian 13 checks the
// signatures with sqv, Ubuntu 24.04 with gpgv 2.4 and Ubuntu 22.04 with gpgv 2.2. On Debian 13 the row runs once more with an imported next
// key whose only binding self-signature is younger than the key (Q-DEB-40): sqv refuses a signature older than every binding of its key, so
// the build waits until the rule holds, and the client that holds only the new key verifies what it publishes.

import (
	"bytes"
	"crypto"
	"net/http"
	"strings"
	"testing"
	"time"

	pgp "github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestDebRotation(t *testing.T) {
	fx := debFx(t)
	for _, img := range aptRotationImages {
		t.Run("R-DEB-017 R-DEB-035 "+img.name+" the table of the rotation with a generated next key", func(t *testing.T) {
			debRotate(t, fx, img, false)
		})
	}
	t.Run("R-DEB-017 R-DEB-016 R-DEB-035 debian13 the table of the rotation with an imported next key whose only self-signature is fresh", func(t *testing.T) {
		debRotate(t, fx, aptDebian13, true)
	})
}

func debRotate(t *testing.T, fx *debFixture, img aptImage, imported bool) {
	purpose := "rot-" + img.name
	if imported {
		purpose += "-imp"
	}
	repo := fx.localRepo(t, purpose, nil)
	p := newDebPackage(t, fx.name(purpose), "1.0")
	fx.awaitPublished(t, repo, debSuite, fx.upload(t, repo, p, debSuite))

	oldKey := fx.keyIn(t, repo, "primary")
	c := fx.client(t, img)
	c.configure(repo) // the source, the credential and (replaced in every cell below) the key file
	keyFile := debKeyFile(repo)

	// A cell: the client's keyring is replaced and its lists are gone, so that the update verifies the release again.
	cell := func(what, keyring string, verifies bool) {
		t.Helper()
		aptWrite(t, img, keyFile, []byte(keyring))
		c.must(`rm -rf /var/lib/apt/lists/*`)
		if verifies {
			if out, err := c.update(); err != nil {
				t.Fatalf("%s: apt-get update --error-on=any on %s failed, want a verified update: %v\n%s", what, img.name, err, tailLines(out, 30))
			}
			return
		}
		c.mustFailVerification(what)
	}

	// before step 1: the old key signs, the client that holds it verifies
	cell("before the next key, the old key only", oldKey.PublicKey, true)

	// step 1: create the next key (generated, or imported with a fresh self-signature); both keys sign from the build it enqueues
	body := map[string]any{"generate": true}
	if imported {
		body = map[string]any{"privateKey": debImportedKeyWithFreshBinding(t)}
	}
	fx.adm.do(http.MethodPost, repoAPI(repo)+"/deb/signing-keys", body).want(t, "create the next key of "+repo, 200, 201)
	next := fx.keyIn(t, repo, "next")
	if imported && !next.BoundSince.After(next.KeyCreatedAt.Add(30*time.Minute)) {
		t.Fatalf("the imported key was created %s and bound %s: its self-signature is not fresh", next.KeyCreatedAt, next.BoundSince)
	}
	cur := fx.indexState(t, repo, debSuite, 0)
	// An imported key whose self-signature is seconds old signs about a minute after the import (R-DEB-016): the wait is that long.
	signedByBoth := fx.awaitSuite(t, repo, debSuite, cur.ChangeSeq, 4*time.Minute, func(s debSuiteState) bool {
		return s.PublishedSeq >= cur.ChangeSeq && len(s.SignedBy) == 2
	})
	for _, k := range []debKey{oldKey, next} {
		if !containsFold(signedByBoth.SignedBy, k.Fingerprint) {
			t.Fatalf("the suite is signed by %v, want both %s and %s", signedByBoth.SignedBy, oldKey.Fingerprint, next.Fingerprint)
		}
	}
	both := string(fx.signingKeyFile(t, repo)) // step 2 of the table: the operator replaces the keyring file while both keys sign
	cell("both keys sign, the old key only", oldKey.PublicKey, true)
	cell("both keys sign, both keys", both, true)
	cell("both keys sign, the new key only", next.PublicKey, true)

	// step 3: promote; the old key stops signing
	fx.adm.do(http.MethodPost, repoAPI(repo)+"/deb/signing-keys/promote", nil).want(t, "promote the next key of "+repo, 200)
	cur = fx.indexState(t, repo, debSuite, 0)
	signedByNew := fx.awaitSuite(t, repo, debSuite, cur.ChangeSeq, 4*time.Minute, func(s debSuiteState) bool {
		return s.PublishedSeq >= cur.ChangeSeq && len(s.SignedBy) == 1
	})
	if !containsFold(signedByNew.SignedBy, next.Fingerprint) {
		t.Fatalf("after the promotion the suite is signed by %v, want %s", signedByNew.SignedBy, next.Fingerprint)
	}
	cell("after the promotion, the old key only", oldKey.PublicKey, false)
	cell("after the promotion, both keys", both, true)
	cell("after the promotion, the new key only", next.PublicKey, true)

	// the repository still serves the package, now to the client that trusts the new key
	aptWrite(t, img, keyFile, []byte(next.PublicKey))
	c.mustUpdate()
	c.mustInstallAndRun(p.name+"="+p.version, p.name, p.line())
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// debImportedKeyWithFreshBinding makes the armored secret key of an operator who removed an expiry with GnuPG just before the import: an
// EdDSALegacy key created an hour ago whose only binding self-signature is thirty seconds old (Q-DEB-40).
func debImportedKeyWithFreshBinding(t testing.TB) string {
	t.Helper()
	now := time.Now()
	created := now.Add(-time.Hour).UTC().Truncate(time.Second)
	bound := now.Add(-30 * time.Second).UTC().Truncate(time.Second)
	cfg := &packet.Config{
		Algorithm:   packet.PubKeyAlgoEdDSA,
		Curve:       packet.Curve25519,
		DefaultHash: crypto.SHA512,
		Time:        func() time.Time { return created },
	}
	e, err := pgp.NewEntity("trove e2e imported key", "", "", cfg)
	if err != nil {
		t.Fatalf("make the key: %v", err)
	}
	e.Subkeys = nil // a repository key only signs
	for _, id := range e.Identities {
		sig := *id.SelfSignature
		sig.CreationTime = bound
		if err := sig.SignUserId(id.Name, e.PrimaryKey, e.PrivateKey, &packet.Config{DefaultHash: id.SelfSignature.Hash, Time: func() time.Time { return bound }}); err != nil {
			t.Fatalf("bind the key again: %v", err)
		}
		id.SelfSignature = &sig
		id.Signatures = []*packet.Signature{&sig}
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, pgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
