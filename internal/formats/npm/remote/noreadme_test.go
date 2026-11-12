package remote_test

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/npm/remote"
)

// TestR_NPM_010_TheReadmeOfAVersionManifestIsWikiContent: with NoReadme a rendering leaves readme and readmeFilename out of every
// version entry, in the full document and in the single-version document, and keeps the rest (R-AUTHZ-034, Q-NPM-88).
func TestR_NPM_010_TheReadmeOfAVersionManifestIsWikiContent(t *testing.T) {
	const doc = `{"name":"p","dist-tags":{"latest":"1.0.0"},"readme":"top","readmeFilename":"README.md","versions":{"1.0.0":{"name":"p","version":"1.0.0","description":"d","readme":"secret text","readmeFilename":"README.md","dist":{"shasum":"x"}}}}`
	for _, deny := range []bool{false, true} {
		p, err := remote.Parse([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		p.NoReadme = deny
		full, err := p.Full("p", "https://t/p/-/", "")
		if err != nil {
			t.Fatal(err)
		}
		one, ok, err := p.Version("p", "https://t/p/-/", "1.0.0")
		if err != nil || !ok {
			t.Fatal(err, ok)
		}
		for name, body := range map[string]string{"full": string(full), "version": string(one)} {
			if has := strings.Contains(body, "secret text"); has == deny {
				t.Errorf("%s, denied=%v: the manifest README present=%v", name, deny, has)
			}
			if !strings.Contains(body, `"description":"d"`) {
				t.Errorf("%s: the rest of the manifest changed: %s", name, body)
			}
		}
		if deny && strings.Contains(string(one), "readmeFilename") {
			t.Error("readmeFilename stayed in the version document")
		}
	}
}
