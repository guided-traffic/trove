//go:build integration && ociconformance

package oci

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestOCIConformance runs the OCI distribution conformance suite (R-OCI-035) in its four categories - pull, push, content
// discovery, content management - against one replica of the integration stack, once through a local repository and once through
// a virtual one. The suite is compiled from the pinned tag by `make oci-conformance`, which sets OCI_CONFORMANCE_BIN to the test
// binary and OCI_CONFORMANCE_OUT to the directory for the JUnit reports (<out>/<run>/junit.xml, uploaded by CI). A failing
// specification fails the test; a skipped specification fails it too unless a documented switch of R-OCI-035 skips it.
//
// The test has its own build tag: it needs the compiled binary, and a run without it would have to skip.
func TestOCIConformance(t *testing.T) {
	bin, out := os.Getenv("OCI_CONFORMANCE_BIN"), os.Getenv("OCI_CONFORMANCE_OUT")
	if bin == "" || out == "" {
		t.Fatal("OCI_CONFORMANCE_BIN and OCI_CONFORMANCE_OUT name the compiled suite and the report directory: run `make oci-conformance`")
	}
	listen := testenv.FreeAddr(t)
	p := newProto(t, protoOptions{replicas: 1, baseURL: "http://" + listen, listen: listen, setup: func(p *proto) {
		// The suite's client holds every action the suite exercises on the three repositories.
		u := p.st.User("conformance")
		p.st.Policy("p-conformance", []string{"read", "deploy", "overwrite", "delete"}, []string{repoLocal, repoOther, repoVirt}, u)
		p.troveToken["conformance"] = p.st.Token(u, scope.Default())
	}})
	secret := p.troveToken["conformance"]
	// Content management is not run through the virtual address: a delete through a virtual repository answers 405 by design
	// (R-OCI-028, ADR-180); the suite's four categories run through the local repository, the virtual one's delete target.
	for _, run := range []struct {
		name, repo string
		management bool
	}{{"local", repoLocal, true}, {"virtual", repoVirt, false}} {
		t.Run(run.name, func(t *testing.T) {
			dir := filepath.Join(out, run.name)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-test.v", "-test.timeout=24m")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"OCI_ROOT_URL=http://"+listen,
				"OCI_NAMESPACE="+project+"/"+run.repo+"/conformance",
				"OCI_CROSSMOUNT_NAMESPACE="+project+"/"+repoOther+"/conformance",
				"OCI_USERNAME=conformance", "OCI_PASSWORD="+secret,
				"OCI_TEST_PULL=1", "OCI_TEST_PUSH=1", "OCI_TEST_CONTENT_DISCOVERY=1", "OCI_TEST_CONTENT_MANAGEMENT="+map[bool]string{true: "1", false: "0"}[run.management],
				"OCI_AUTOMATIC_CROSSMOUNT=0", "OCI_DELETE_MANIFEST_BEFORE_BLOBS=1", "OCI_HIDE_SKIPPED_WORKFLOWS=0",
				"OCI_REPORT_DIR="+dir,
			)
			var buf bytes.Buffer
			cmd.Stdout, cmd.Stderr = &buf, &buf
			err := cmd.Run()
			if werr := os.WriteFile(filepath.Join(dir, "output.txt"), buf.Bytes(), 0o600); werr != nil {
				t.Errorf("write the output: %v", werr)
			}
			if err != nil {
				t.Fatalf("the suite through %s failed: %v\n%s", run.repo, err, testenv.Tail(buf.String(), 80))
			}
			failed, skipped, total := readJUnit(t, filepath.Join(dir, "junit.xml"))
			t.Logf("%s: %d specifications, %d failed, %d skipped", run.repo, total, len(failed), len(skipped))
			if total == 0 {
				t.Fatal("the report holds no specification")
			}
			if len(failed) > 0 {
				t.Fatalf("failed specifications: %v", failed)
			}
			for _, s := range skipped {
				if !documentedSkip(s, run.management) {
					t.Errorf("skipped beyond the switches R-OCI-035 documents: %s", s)
				}
			}
		})
	}
}

// documentedSkip reports whether a specification may be skipped. The two setup specs that read their data from the environment
// (OCI_TAG_NAME with OCI_MANIFEST_DIGEST and OCI_BLOB_DIGEST, OCI_TAG_LIST) replace the suite's own pushes and run only when
// those variables are set, which the runner never does. The spec for automatic cross-mounting enabled runs only with
// OCI_AUTOMATIC_CROSSMOUNT=1; the runner sets 0 (R-OCI-035), so its twin for disabled runs. The spec for a cross-mount of a
// nonexistent blob runs only after a mount answered 202; trove answers the mount of a blob the source holds with 201, so the spec
// of that branch ("GET request to test digest within cross-mount namespace") runs instead. With OCI_TEST_CONTENT_MANAGEMENT=0 the
// specs of that category are skipped. Nothing else is skipped.
func documentedSkip(name string, management bool) bool {
	switch {
	case strings.HasSuffix(name, "Pull Setup Get tag name from environment"),
		strings.HasSuffix(name, "Content Discovery Setup Populate registry with test tags (no push)"):
		return true
	case strings.HasSuffix(name, "Cross-mounting without from, and automatic content discovery enabled should return a 201"),
		strings.HasSuffix(name, "Cross-mounting of nonexistent blob should yield session id"):
		return true
	case !management && strings.Contains(name, " Content Management "):
		return true
	}
	return false
}

type junitCase struct {
	Name    string    `xml:"name,attr"`
	Failure *struct{} `xml:"failure"`
	Error   *struct{} `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

// junitReport reads either root of a JUnit file: <testsuites> holding suites, or one <testsuite>.
type junitReport struct {
	Suites []struct {
		Cases []junitCase `xml:"testcase"`
	} `xml:"testsuite"`
	Cases []junitCase `xml:"testcase"`
}

func readJUnit(t *testing.T, path string) (failed, skipped []string, total int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc junitReport
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	cases := doc.Cases
	for _, s := range doc.Suites {
		cases = append(cases, s.Cases...)
	}
	for _, c := range cases {
		total++
		switch {
		case c.Failure != nil || c.Error != nil:
			failed = append(failed, strings.TrimSpace(c.Name))
		case c.Skipped != nil:
			skipped = append(skipped, strings.TrimSpace(c.Name))
		}
	}
	return failed, skipped, total
}
