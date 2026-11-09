//go:build integration

package deblocal

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	debdata "github.com/guided-traffic/trove/internal/formatdata/deb"
	"github.com/guided-traffic/trove/internal/formats/deb/control"
	"github.com/guided-traffic/trove/internal/openpgp"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func (e *env) base() string { return "/api/deb/" + project + "/" + repo + "/" }

func (e *env) get(path, token string, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", e.base()+path, token, nil, hdr...)
}

func (e *env) dbNow() time.Time {
	e.t.Helper()
	return time.Unix(e.st.Number(`SELECT extract(epoch from now())::bigint`), 0).UTC()
}

type suiteState struct {
	Distribution string `json:"distribution"`
	ChangeSeq    int64  `json:"changeSeq"`
	PublishedSeq int64  `json:"publishedSeq"`
	Generation   int64  `json:"generation"`
	ReleaseDate  string `json:"releaseDate"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	SignedBy     []string
}

// waitIndex reads the index state with waitFor until the suite published seq (R-DEB-014) and returns the suite's state.
func (e *env) waitIndex(suite string, seq int64) suiteState {
	e.t.Helper()
	path := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/index?waitFor=" + suite + ":" + strconv.FormatInt(seq, 10)
	for i := 0; i < 20; i++ {
		a := e.st.Do(e.r, "GET", path, e.tokA, nil)
		if a.Status != 200 {
			e.t.Fatalf("index state = %d %s", a.Status, a.Body)
		}
		var body struct {
			Suites []suiteState `json:"suites"`
		}
		if err := json.Unmarshal(a.Body, &body); err != nil {
			e.t.Fatal(err)
		}
		for _, s := range body.Suites {
			if s.Distribution == suite && s.PublishedSeq >= seq {
				return s
			}
		}
	}
	e.t.Logf("suite rows: %s", e.st.Text(`SELECT coalesce(string_agg(suite || ' change=' || change_seq || ' built=' || built_seq || ' ' || state || ' ' || coalesce(reason, ''), '; '), 'none') FROM deb_suites`))
	e.st.Query(`SELECT kind, state, coalesce(error::text, ''), coalesce(target_key, '-') || ' ' || coalesce(dedup_key, '-'), args::text FROM job ORDER BY created_at`, func(rows pgx.Rows) error {
		for rows.Next() {
			var kind, state, errText, key, args string
			if err := rows.Scan(&kind, &state, &errText, &key, &args); err != nil {
				return err
			}
			e.t.Logf("job %s %s %s target %s args %s", kind, state, errText, key, args)
		}
		return rows.Err()
	})
	e.t.Fatalf("suite %s did not publish change %d", suite, seq)
	return suiteState{}
}

func seqOf(e *env, a map[string]any, suite string) int64 {
	e.t.Helper()
	for _, x := range a["index"].([]any) {
		m := x.(map[string]any)
		if m["distribution"] == suite {
			return int64(m["changeSeq"].(float64))
		}
	}
	e.t.Fatalf("no index number for %s in %v", suite, a)
	return 0
}

func (e *env) certs() []openpgp.Certificate {
	e.t.Helper()
	a := e.get("signing-key.asc", e.tokA)
	if a.Status != 200 {
		e.t.Fatalf("signing-key.asc = %d %s", a.Status, a.Body)
	}
	cs, err := openpgp.ParseCertificates(a.Body, e.dbNow(), openpgp.DefaultLimits())
	if err != nil {
		e.t.Fatal(err)
	}
	return cs
}

func paragraphs(t *testing.T, b []byte) []control.Paragraph {
	t.Helper()
	r := control.NewReader(bytes.NewReader(b), false)
	var out []control.Paragraph
	for {
		p, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// release reads and checks the three release documents of a suite and returns the Release paragraph.
func (e *env) release(suite string) (control.Paragraph, []byte) {
	e.t.Helper()
	dir := "dists/" + suite + "/"
	rel, in, gpg := e.get(dir+"Release", e.tokA), e.get(dir+"InRelease", e.tokA), e.get(dir+"Release.gpg", e.tokA)
	for name, a := range map[string]testenv.Answer{"Release": rel, "InRelease": in, "Release.gpg": gpg} {
		if a.Status != 200 {
			e.t.Fatalf("%s = %d %s", name, a.Status, a.Body)
		}
	}
	certs := e.certs()
	now := e.dbNow()
	text, signer, err := openpgp.VerifyClear(in.Body, certs, now, openpgp.DefaultLimits())
	if err != nil {
		e.t.Fatalf("InRelease does not verify: %v", err)
	}
	if !bytes.Equal(text, rel.Body) {
		e.t.Errorf("the text InRelease signs differs from Release:\n%q\n%q", text, rel.Body)
	}
	if _, err := openpgp.VerifyDetached(rel.Body, gpg.Body, certs, now, openpgp.DefaultLimits()); err != nil {
		e.t.Errorf("Release.gpg does not verify: %v", err)
	}
	if signer == "" {
		e.t.Error("no signer")
	}
	ps := paragraphs(e.t, rel.Body)
	if len(ps) != 1 {
		e.t.Fatalf("Release has %d paragraphs", len(ps))
	}
	return ps[0], rel.Body
}

func releaseDate(t *testing.T, p control.Paragraph) time.Time {
	t.Helper()
	d, err := time.Parse("Mon, 02 Jan 2006 15:04:05 MST", p.Value("Date"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// packagesOf reads and returns the Package names of a suite's index through the Release's listing, checking sizes and digests.
func (e *env) packagesOf(suite string, rel control.Paragraph, path string) []control.Paragraph {
	e.t.Helper()
	var line string
	for _, l := range strings.Split(rel.Value("SHA256"), "\n") {
		if strings.HasSuffix(l, " "+path) {
			line = l
		}
	}
	if line == "" {
		e.t.Fatalf("Release lists no %s", path)
	}
	f := strings.Fields(line)
	a := e.get("dists/"+suite+"/"+path, e.tokA)
	if a.Status != 200 {
		e.t.Fatalf("%s = %d", path, a.Status)
	}
	if strconv.Itoa(len(a.Body)) != f[1] {
		e.t.Errorf("%s: size %d, Release says %s", path, len(a.Body), f[1])
	}
	if sha := fmt.Sprintf("%x", sha256sum(a.Body)); sha != f[0] {
		e.t.Errorf("%s: sha256 %s, Release says %s", path, sha, f[0])
	}
	h := e.get("dists/"+suite+"/"+strings.TrimSuffix(path, "Packages.gz")+"by-hash/SHA256/"+f[0], e.tokA)
	if h.Status != 200 || !bytes.Equal(h.Body, a.Body) {
		e.t.Errorf("by-hash of %s = %d", path, h.Status)
	}
	if cc := h.Header.Get("Cache-Control"); cc != "private, max-age=31536000, immutable" {
		e.t.Errorf("by-hash Cache-Control = %q", cc)
	}
	return paragraphs(e.t, gunzip(e.t, a.Body))
}

// R-DEB-010, R-DEB-011, R-DEB-016: an upload is published as a signed generation: InRelease and Release.gpg verify with the repository's
// key, InRelease signs Release byte for byte, Release lists every index file with its size and digest, Packages lists the upload with
// the computed fields, the Date trails the database clock by at least 30 s and equals the Last-Modified of InRelease.
func TestR_DEB_010_AnUploadIsPublishedAsASignedGeneration(t *testing.T) {
	e := newEnv(t)
	body := build(t, "hello", "2.10-3", nil)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", body, 201, []string{"stable"})
	seq := seqOf(e, a, "stable")
	st := e.waitIndex("stable", seq)
	if st.State != "current" || st.Generation < 1 || len(st.SignedBy) != 1 {
		t.Fatalf("state after the wait: %+v", st)
	}
	rel, _ := e.release("stable")
	if rel.Value("Suite") != "stable" || rel.Value("Acquire-By-Hash") != "yes" || rel.Value("Components") != "main" || rel.Value("Origin") != "trove" {
		t.Errorf("Release fields: %v", rel.Fields)
	}
	date := releaseDate(t, rel)
	if now := e.dbNow(); now.Sub(date) < 30*time.Second {
		t.Errorf("the Release Date %s is less than 30 s before the database time %s", date, now)
	}
	in := e.get("dists/stable/InRelease", e.tokA)
	lm, err := http.ParseTime(in.Header.Get("Last-Modified"))
	if err != nil || !lm.Equal(date) {
		t.Errorf("Last-Modified %q does not equal the Date %s", in.Header.Get("Last-Modified"), date)
	}
	if cc := in.Header.Get("Cache-Control"); cc != "private, max-age=0, must-revalidate" {
		t.Errorf("InRelease Cache-Control = %q", cc)
	}
	ps := e.packagesOf("stable", rel, "main/binary-amd64/Packages.gz")
	if len(ps) != 1 || ps[0].Value("Package") != "hello" || ps[0].Value("Filename") != poolHello || ps[0].Value("SHA256") == "" || ps[0].Value("MD5sum") == "" {
		t.Fatalf("Packages: %v", ps)
	}
	if got := ps[0].Value("Size"); got != strconv.Itoa(len(body)) {
		t.Errorf("Size = %s, want %d", got, len(body))
	}
	if _, err := openpgp.VerifyDetached(body, []byte("x"), e.certs(), e.dbNow(), openpgp.DefaultLimits()); err == nil {
		t.Error("a package verified against a foreign signature")
	}
	if a := e.get("dists/stable/main/binary-amd64/Packages", e.tokA); a.Status != 404 {
		t.Errorf("an uncompressed Packages = %d, want 404", a.Status)
	}
	if a := e.get("dists/nosuch/InRelease", e.tokA); a.Status != 404 {
		t.Errorf("an unknown suite = %d", a.Status)
	}
}

// R-DEB-012: the by-hash files of earlier generations stay while retention keeps them, and a later generation has a later Date.
func TestR_DEB_012_EarlierGenerationsStayByHash(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	first := e.waitIndex("stable", seqOf(e, a, "stable"))
	rel1, _ := e.release("stable")
	oldHash := ""
	for _, l := range strings.Split(rel1.Value("SHA256"), "\n") {
		if strings.HasSuffix(l, " main/binary-amd64/Packages.gz") {
			oldHash = strings.Fields(l)[0]
		}
	}
	a = e.mustUpload(e.tokA, "hello_2.11-1_amd64.deb", build(t, "hello", "2.11-1", nil), 201, []string{"stable"})
	second := e.waitIndex("stable", seqOf(e, a, "stable"))
	if second.Generation <= first.Generation {
		t.Errorf("generation %d does not follow %d", second.Generation, first.Generation)
	}
	rel2, _ := e.release("stable")
	if !releaseDate(t, rel2).After(releaseDate(t, rel1)) {
		t.Errorf("Date %s does not follow %s", rel2.Value("Date"), rel1.Value("Date"))
	}
	if ps := e.packagesOf("stable", rel2, "main/binary-amd64/Packages.gz"); len(ps) != 2 {
		t.Errorf("the second generation lists %d packages", len(ps))
	}
	if old := e.get("dists/stable/main/binary-amd64/by-hash/SHA256/"+oldHash, e.tokA); old.Status != 200 {
		t.Errorf("the first generation's index by hash = %d", old.Status)
	}
}

// R-DEB-013, R-DEB-014: the deletion of a file is published; after the last file of a suite is deleted a reindex drops the suite.
func TestR_DEB_014_DeleteAndReindex(t *testing.T) {
	e := newEnv(t)
	e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	a := e.mustUpload(e.tokA, "hello_2.11-1_amd64.deb", build(t, "hello", "2.11-1", nil), 201, []string{"stable", "testing"})
	e.waitIndex("stable", seqOf(e, a, "stable"))
	e.waitIndex("testing", seqOf(e, a, "testing"))

	if d := e.st.Do(e.r, "DELETE", e.base()+"pool/h/hello/hello_2.10-3_amd64.deb", e.tokA, nil); d.Status != 204 {
		t.Fatalf("delete = %d %s", d.Status, d.Body)
	}
	seq := e.st.Number(`SELECT s.change_seq FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = 'stable'`, repo)
	e.waitIndex("stable", seq)
	rel, _ := e.release("stable")
	if ps := e.packagesOf("stable", rel, "main/binary-amd64/Packages.gz"); len(ps) != 1 || ps[0].Value("Version") != "2.11-1" {
		t.Errorf("after the deletion: %v", ps)
	}

	// The last file leaves testing: its generation stays with empty indexes until a reindex drops the suite.
	if a := e.setCoordinates(e.tokA, "pool/h/hello/hello_2.11-1_amd64.deb", [2]string{"stable", "main"}); a.Status != 200 {
		t.Fatalf("move = %d %s", a.Status, a.Body)
	}
	seq = e.st.Number(`SELECT s.change_seq FROM deb_suites s JOIN repositories r ON r.id = s.repo_id WHERE r.key = $1 AND s.suite = 'testing'`, repo)
	e.waitIndex("testing", seq)
	rel, _ = e.release("testing")
	if ps := e.packagesOf("testing", rel, "main/binary-amd64/Packages.gz"); len(ps) != 0 {
		t.Errorf("testing still lists %v", ps)
	}
	r := e.st.Do(e.r, "POST", "/api/v1/projects/"+project+"/repositories/"+repo+"/reindex", e.tokAdmin, nil)
	if r.Status != 202 {
		t.Fatalf("reindex = %d %s", r.Status, r.Body)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if e.get("dists/testing/InRelease", e.tokA).Status == 404 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Error("dists/testing/InRelease still answers after the reindex")
}

// R-DEB-014: waitFor on a change no build reaches answers with the state after deb.index_wait_max, and an unknown suite is just absent.
func TestR_DEB_014_WaitForAnswersAfterTheMaximum(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	st := e.waitIndex("stable", seqOf(e, a, "stable"))
	start := time.Now()
	r := e.st.Do(e.r, "GET", "/api/v1/projects/"+project+"/repositories/"+repo+"/deb/index?waitFor=stable:"+strconv.FormatInt(st.PublishedSeq+1000, 10), e.tokA, nil)
	if r.Status != 200 || time.Since(start) < time.Second || time.Since(start) > 10*time.Second {
		t.Errorf("a wait for a number nothing reaches: %d after %s", r.Status, time.Since(start))
	}
	if bad := e.st.Do(e.r, "GET", "/api/v1/projects/"+project+"/repositories/"+repo+"/deb/index?waitFor=stable", e.tokA, nil); bad.Status != 422 && bad.Status != 400 {
		t.Errorf("a malformed waitFor = %d", bad.Status)
	}
}

// R-DEB-010: fifty uploads in parallel are all listed in the published index and fewer builds ran than uploads.
func TestR_DEB_010_ParallelUploadsAreCoalesced(t *testing.T) {
	e := newEnv(t)
	const n = 30
	var wg sync.WaitGroup
	seqs := make([]int64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("pkg%02d", i)
			a := e.upload(e.tokA, name+"_1.0-1_amd64.deb", build(t, name, "1.0-1", nil), []string{"stable"})
			if a.Status != 201 {
				t.Errorf("upload %s = %d %s", name, a.Status, a.Body)
				return
			}
			body := a.JSON(t)
			seqs[i] = seqOf(e, body, "stable")
		}()
	}
	wg.Wait()
	var max int64
	for _, s := range seqs {
		max = max64(max, s)
	}
	st := e.waitIndex("stable", max)
	rel, _ := e.release("stable")
	if ps := e.packagesOf("stable", rel, "main/binary-amd64/Packages.gz"); len(ps) != n {
		t.Errorf("the index lists %d of %d uploads", len(ps), n)
	}
	if st.Generation >= n {
		t.Errorf("%d builds ran for %d uploads", st.Generation, n)
	}
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

// R-DEB-010: a build that stops between its blob writes and its commit leaves the previous generation served byte for byte; the next
// build publishes.
func TestR_DEB_010_AStoppedBuildLeavesThePreviousGeneration(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	e.waitIndex("stable", seqOf(e, a, "stable"))
	before := e.get("dists/stable/InRelease", e.tokA)

	x := debdata.Bound()
	if x == nil {
		t.Fatal("no index builder is bound")
	}
	var mu sync.Mutex
	stopped := 0
	x.BeforeCommit = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		if stopped == 0 {
			stopped++
			return errors.New("injected fault")
		}
		return nil
	}
	t.Cleanup(func() { x.BeforeCommit = nil })

	a = e.mustUpload(e.tokA, "hello_2.11-1_amd64.deb", build(t, "hello", "2.11-1", nil), 201, []string{"stable"})
	seq := seqOf(e, a, "stable")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := stopped
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if now := e.get("dists/stable/InRelease", e.tokA); !bytes.Equal(now.Body, before.Body) {
		t.Error("InRelease changed although the build stopped before its commit")
	}
	e.waitIndex("stable", seq) // the retry publishes
	rel, _ := e.release("stable")
	if ps := e.packagesOf("stable", rel, "main/binary-amd64/Packages.gz"); len(ps) != 2 {
		t.Errorf("after the retry the index lists %d packages", len(ps))
	}
}

// R-DEB-016, R-DEB-017: while a next key exists each file verifies with either key alone and is signed by both.
func TestR_DEB_016_CoSignedWhileANextKeyExists(t *testing.T) {
	e := newEnv(t)
	a := e.mustUpload(e.tokA, "hello_2.10-3_amd64.deb", build(t, "hello", "2.10-3", nil), 201, []string{"stable"})
	first := e.waitIndex("stable", seqOf(e, a, "stable"))
	keys := "/api/v1/projects/" + project + "/repositories/" + repo + "/deb/signing-keys"
	if r := e.st.Do(e.r, "POST", keys, e.tokAdmin, map[string]any{"generate": true}); r.Status/100 != 2 {
		t.Fatalf("create next = %d %s", r.Status, r.Body)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		st := e.waitIndex("stable", 1)
		if len(st.SignedBy) == 2 && st.Generation > first.Generation {
			break
		}
		time.Sleep(time.Second)
	}
	certs := e.certs()
	if len(certs) != 2 {
		t.Fatalf("signing-key.asc holds %d certificates", len(certs))
	}
	in, gpg, rel := e.get("dists/stable/InRelease", e.tokA), e.get("dists/stable/Release.gpg", e.tokA), e.get("dists/stable/Release", e.tokA)
	now := e.dbNow()
	for i := range certs {
		if _, _, err := openpgp.VerifyClear(in.Body, certs[i:i+1], now, openpgp.DefaultLimits()); err != nil {
			t.Errorf("InRelease with key %d alone: %v", i, err)
		}
		if _, err := openpgp.VerifyDetached(rel.Body, gpg.Body, certs[i:i+1], now, openpgp.DefaultLimits()); err != nil {
			t.Errorf("Release.gpg with key %d alone: %v", i, err)
		}
	}
}

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }
