package ansible

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/ansibletest"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

// noShadow lets a first publish of a name proceed: the shadowing check has nothing to ask in a repository without remote members.
type noShadow struct{ formattest.Resolver }

func (noShadow) ShadowCheck(context.Context, *formats.Request, string) (formats.ShadowOutcome, error) {
	return formats.ShadowOutcome{Proceed: true}, nil
}

// hostOptions is the options of every Host of the tests: the in-memory twin of the data port and a resolver without remote members.
func hostOptions() formattest.HostOptions {
	return formattest.HostOptions{Data: &ansibletest.Store{}, Resolver: noShadow{}}
}

// collectionSpec is the archive of the tests: acme.tools at the version, with a README and a runtime file.
func collectionSpec(ver string) collectiontest.Spec {
	return collectiontest.Spec{
		Namespace: "acme", Name: "tools", Version: ver,
		Dependencies: map[string]string{"community.general": ">=1.0.0"},
		Files: map[string]string{
			"README.md":            "# tools " + ver + "\n",
			"plugins/modules/x.py": "print('x')\n",
			"meta/runtime.yml":     "---\nrequires_ansible: '>=2.14.0'\n",
		},
	}
}

// publishForm builds the multipart body of a publish with the parts in the order the client sends them (sha256 first) or the order of
// the documentation (file first); sha is the digest sent, "" for the digest of the archive.
func publishForm(archive []byte, shaFirst bool, sha string) (body io.Reader, contentType string) {
	if sha == "" {
		sum := sha256.Sum256(archive)
		sha = hex.EncodeToString(sum[:])
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	file := func() {
		fw, _ := mw.CreateFormFile("file", "archive.tar.gz")
		_, _ = fw.Write(archive)
	}
	digest := func() { _ = mw.WriteField("sha256", sha) }
	if shaFirst {
		digest()
		file()
	} else {
		file()
		digest()
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

// publish publishes an archive through the route of the Host and returns the response.
func publish(h *formattest.Host, archive []byte, shaFirst bool, sha string) (*formattest.Response, error) {
	body, ct := publishForm(archive, shaFirst, sha)
	return h.Serve(context.Background(), http.MethodPost, "v3/artifacts/collections/", body, false, func(r *http.Request) { r.Header.Set("Content-Type", ct) })
}

func rowsAudit() rows.Audit {
	return rows.Audit{RepoKey: "it/main", Actor: rows.Actor{Kind: "user", ID: "u1"}}
}

// publishVersion publishes acme.tools at the version and fails on any answer but 202.
func publishVersion(ver string) func(h *formattest.Host) error {
	return func(h *formattest.Host) error {
		r, err := publish(h, collectiontest.Build(collectionSpec(ver)), true, "")
		if err == nil && r.Code != http.StatusAccepted {
			err = fmt.Errorf("publish %s: status %d: %s", ver, r.Code, r.Body.String())
		}
		return err
	}
}
