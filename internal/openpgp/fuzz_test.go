package openpgp

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

// armoredDebianKeyring is the archive keyring of debian:13 (package debian-archive-keyring 2025.1, whose keys "don't fall under
// any copyright", see testdata/README.md) in the armored form an operator pastes into deb.upstreamKeys.
func armoredDebianKeyring(tb testing.TB) []byte {
	tb.Helper()
	raw, err := os.ReadFile("testdata/debian-archive-keyring.pgp")
	if err != nil {
		tb.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, "PGP PUBLIC KEY BLOCK", nil)
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return buf.Bytes()
}

func seedKeys(f *testing.F) (secret, public []byte) {
	f.Helper()
	secret, public, _, err := Generate("trove p/r", base.Add(-time.Hour))
	if err != nil {
		f.Fatal(err)
	}
	return secret, public
}

func FuzzParseCertificates(f *testing.F) {
	_, public := seedKeys(f)
	f.Add(public)
	f.Add([]byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n=\n-----END PGP PUBLIC KEY BLOCK-----\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		certs, err := ParseCertificates(in, base, Limits{})
		if err == nil && len(certs) == 0 {
			t.Fatal("success with no certificate")
		}
	})
}

func FuzzParseSecret(f *testing.F) {
	secret, _ := seedKeys(f)
	f.Add(secret)
	f.Fuzz(func(t *testing.T, in []byte) {
		if k, err := ParseSecret(in, []byte("pw"), base, Limits{}); err == nil && k == nil {
			t.Fatal("success with no key")
		}
	})
}

func FuzzVerifyClear(f *testing.F) {
	secret, public, _, err := Generate("trove p/r", base.Add(-time.Hour))
	if err != nil {
		f.Fatal(err)
	}
	k, err := ParseSecret(secret, nil, base, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	certs, err := ParseCertificates(public, base, Limits{})
	if err != nil {
		f.Fatal(err)
	}
	signed, err := ClearSign([]byte(release), []*Key{k}, base.Add(-time.Minute))
	if err != nil {
		f.Fatal(err)
	}
	detached, err := DetachSign([]byte(release), []*Key{k}, base.Add(-time.Minute))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(signed, detached)
	f.Fuzz(func(t *testing.T, msg, sig []byte) {
		_, _, _ = VerifyClear(msg, certs, base, Limits{})
		_, _ = VerifyDetached([]byte(release), sig, certs, base, Limits{})
	})
}
