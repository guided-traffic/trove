package importer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ItemError is a failure of one version that the format knows the reason of; the runner reports its code.
type ItemError struct {
	Code   string
	Detail string
}

func (e *ItemError) Error() string { return e.Code + ": " + e.Detail }

// NPM is the npm format of the importer (R-IMP-005, R-IMP-009): it reads the source as a package manager does and publishes
// with the request of `npm publish`.
type NPM struct{}

var npmName = regexp.MustCompile(`^(@[A-Za-z0-9][A-Za-z0-9._~-]*/)?[A-Za-z0-9][A-Za-z0-9._~-]*$`)

// ID implements Format.
func (NPM) ID() string { return "npm" }

// ValidName implements Format.
func (NPM) ValidName(name string) bool { return len(name) <= 214 && npmName.MatchString(name) }

// maxScan bounds the decompressed bytes the scan for a tarball's package.json reads.
const maxScan = 2 << 30

// maxPackument bounds a package document of the source.
const maxPackument = 256 << 20

func npmURL(base *url.URL, name string, rest ...string) string {
	u := strings.TrimSuffix(base.String(), "/") + "/" + strings.ReplaceAll(name, "/", "%2f")
	for _, r := range rest {
		u += "/" + r
	}
	return u
}

// Preflight implements Format: whoami answers for a token the repository accepts.
func (NPM) Preflight(ctx context.Context, dst *TargetClient) error {
	resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: strings.TrimSuffix(dst.Base.String(), "/") + "/-/whoami", Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return err
	}
	switch resp.Status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return &Refusal{Status: resp.Status, Code: "unauthorized", Msg: "the token is not accepted"}
	}
	return fmt.Errorf("the repository answered %d to whoami: is the address an npm repository of trove that the token can read?", resp.Status)
}

type npmSearch struct {
	Objects []struct {
		Package struct {
			Name string `json:"name"`
		} `json:"package"`
	} `json:"objects"`
	Total int `json:"total"`
}

// Search implements Format with the search endpoint registries share (GET -/v1/search).
func (NPM) Search(ctx context.Context, src *SourceClient, q string) ([]string, error) {
	var names []string
	const size = 250
	for from, pages := 0, 0; pages < 400; pages++ {
		var res npmSearch
		u := strings.TrimSuffix(src.Origin.String(), "/") + "/-/v1/search?text=" + url.QueryEscape(q) + "&size=" + strconv.Itoa(size) + "&from=" + strconv.Itoa(from)
		found, err := src.GetJSON(ctx, u, "application/json", 32<<20, &res)
		if err != nil {
			return nil, err
		}
		if !found || len(res.Objects) == 0 {
			break
		}
		for _, o := range res.Objects {
			names = append(names, o.Package.Name)
		}
		from += len(res.Objects)
		if from >= res.Total {
			break
		}
	}
	return names, nil
}

type npmDoc struct {
	Name     string                     `json:"name"`
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]json.RawMessage `json:"versions"`
	Time     map[string]string          `json:"time"`
}

type npmVersionDoc struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	Deprecated json.RawMessage `json:"deprecated"`
	Dist       struct {
		Integrity    string          `json:"integrity"`
		Shasum       string          `json:"shasum"`
		Tarball      string          `json:"tarball"`
		Attestations json.RawMessage `json:"attestations"`
	} `json:"dist"`
}

// strongest picks the strongest digest an npm version announces: the best algorithm of dist.integrity, else dist.shasum.
func strongest(integrity, shasum string) Digest {
	rank := map[string]int{"sha512": 4, "sha384": 3, "sha256": 2, "sha1": 1}
	var best Digest
	for _, tok := range strings.Fields(integrity) {
		alg, rest, ok := strings.Cut(tok, "-")
		alg = strings.ToLower(alg)
		if !ok || rank[alg] == 0 || rank[alg] <= rank[best.Alg] {
			continue
		}
		b64, _, _ := strings.Cut(rest, "?")
		sum, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			if sum, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
				continue
			}
		}
		if len(sum) != algSize(alg) {
			continue
		}
		best = Digest{Alg: alg, Sum: sum}
	}
	if best.Alg == "" && shasum != "" {
		if sum, err := hex.DecodeString(strings.ToLower(shasum)); err == nil && len(sum) == algSize("sha1") {
			best = Digest{Alg: "sha1", Sum: sum}
		}
	}
	return best
}

// algSize is the length of a digest in bytes by algorithm; 0 for an algorithm the importer does not know.
func algSize(alg string) int {
	switch alg {
	case "sha512":
		return 64
	case "sha384":
		return 48
	case "sha256":
		return 32
	case "sha1":
		return 20
	}
	return 0
}

// Plan implements Format: the full package document, the versions in the order of their publication.
func (NPM) Plan(ctx context.Context, src *SourceClient, name string) (*Plan, error) {
	var doc npmDoc
	found, err := src.GetJSON(ctx, npmURL(src.Origin, name), "application/json", maxPackument, &doc)
	if err != nil {
		return nil, err
	}
	p := &Plan{Name: name, Tags: map[string]string{}}
	if !found {
		return p, nil
	}
	if doc.Name != name {
		return nil, fmt.Errorf("the document names the package %q", doc.Name)
	}
	var deprecated, attested []string
	for key, raw := range doc.Versions {
		var vd npmVersionDoc
		if err := json.Unmarshal(raw, &vd); err != nil {
			return nil, fmt.Errorf("the version %q is not a document: %w", key, err)
		}
		if vd.Version != key || vd.Name != name {
			return nil, fmt.Errorf("the version %q of the document names %s@%s", key, vd.Name, vd.Version)
		}
		v := Version{Version: key, Listed: true, URL: vd.Dist.Tarball, Want: strongest(vd.Dist.Integrity, vd.Dist.Shasum), Meta: raw}
		if t, err := time.Parse(time.RFC3339, doc.Time[key]); err == nil {
			v.Published = t
		}
		var msg string
		if json.Unmarshal(vd.Deprecated, &msg) == nil && msg != "" {
			v.Deprecated = msg
			deprecated = append(deprecated, key)
		}
		if len(vd.Dist.Attestations) > 0 && string(vd.Dist.Attestations) != "null" {
			attested = append(attested, key)
		}
		p.Versions = append(p.Versions, v)
	}
	sortVersions(p.Versions)
	for t, v := range doc.DistTags {
		p.Tags[t] = v
	}
	sort.Strings(deprecated)
	sort.Strings(attested)
	if len(deprecated) > 0 {
		p.NotKept = append(p.NotKept, "deprecation of "+strings.Join(deprecated, ", ")+" (trove has no way to set a deprecation yet)")
	}
	if len(attested) > 0 {
		p.NotKept = append(p.NotKept, "provenance attestations of "+strings.Join(attested, ", "))
	}
	return p, nil
}

type npmTargetDoc struct {
	DistTags map[string]string `json:"dist-tags"`
	Versions map[string]struct {
		Dist struct {
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	} `json:"versions"`
}

// Held implements Format: the package document of the target.
func (NPM) Held(ctx context.Context, dst *TargetClient, name string) (map[string]Held, map[string]string, error) {
	resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: npmURL(dst.Base, name), Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return nil, nil, err
	}
	switch resp.Status {
	case http.StatusOK:
	case http.StatusNotFound:
		return map[string]Held{}, map[string]string{}, nil
	default:
		return nil, nil, refusal(resp)
	}
	var doc npmTargetDoc
	if err := json.Unmarshal(resp.Body, &doc); err != nil {
		return nil, nil, fmt.Errorf("the target's document of %s is not JSON: %w", name, err)
	}
	held := map[string]Held{}
	for v, d := range doc.Versions {
		h := Held{Digests: map[string][]byte{}, Listed: true}
		if best := strongest(d.Dist.Integrity, ""); best.Alg == "sha512" {
			h.Digests["sha512"] = best.Sum
		}
		if sum, err := hex.DecodeString(d.Dist.Shasum); err == nil && len(sum) == 20 {
			h.Digests["sha1"] = sum
		}
		held[v] = h
	}
	if doc.DistTags == nil {
		doc.DistTags = map[string]string{}
	}
	return held, doc.DistTags, nil
}

// tarballManifest reads the package.json of an npm tarball the way the publish does: the file one directory below the root.
func tarballManifest(f *File) (map[string]any, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, &ItemError{Code: CodeIdentityMismatch, Detail: "the file is not a gzip archive"}
	}
	defer zr.Close()
	// The target stops reading a tarball at 2 GiB unpacked (npm.publish.max_unpacked_size); the scan for the manifest does too.
	tr := tar.NewReader(io.LimitReader(zr, maxScan))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, &ItemError{Code: CodeIdentityMismatch, Detail: "the archive holds no package.json"}
		}
		if err != nil {
			return nil, &ItemError{Code: CodeIdentityMismatch, Detail: "the file is not a tar archive"}
		}
		n := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag != tar.TypeReg || strings.Count(n, "/") != 1 || !strings.HasSuffix(n, "/package.json") {
			continue
		}
		if h.Size > 1<<20 {
			return nil, &ItemError{Code: CodeTooLarge, Detail: "the package.json is larger than 1 MiB"}
		}
		b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, &ItemError{Code: CodeIdentityMismatch, Detail: "the archive is cut"}
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, &ItemError{Code: CodeIdentityMismatch, Detail: "the package.json is not JSON"}
		}
		return m, nil
	}
}

// b64Reader streams the base64 form of a reader without holding it.
type b64Reader struct {
	src io.Reader
	in  []byte
	out []byte
	pos int
	err error
}

func newB64Reader(src io.Reader) *b64Reader {
	return &b64Reader{src: src, in: make([]byte, 3*8192), out: make([]byte, 0, 4*8192)}
}

func (r *b64Reader) Read(p []byte) (int, error) {
	for r.pos >= len(r.out) {
		if r.err != nil {
			return 0, r.err
		}
		n, err := io.ReadFull(r.src, r.in)
		r.out = r.out[:base64.StdEncoding.EncodedLen(n)]
		base64.StdEncoding.Encode(r.out, r.in[:n])
		r.pos = 0
		switch {
		case err == nil:
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			r.err = io.EOF
		default:
			r.err = err
		}
	}
	n := copy(p, r.out[r.pos:])
	r.pos += n
	return n, nil
}

// Publish implements Format: the request `npm publish` sends. The manifest is the package.json of the tarball with the
// digests of the verified file; the tarball goes along base64-encoded and is streamed from the spool file.
func (NPM) Publish(ctx context.Context, dst *TargetClient, p *Plan, v Version, f *File, tag string) error {
	m, err := tarballManifest(f)
	if err != nil {
		return err
	}
	if m["name"] != p.Name || m["version"] != v.Version {
		return &ItemError{Code: CodeIdentityMismatch, Detail: fmt.Sprintf("the package.json names %v@%v, the source listed %s@%s", m["name"], m["version"], p.Name, v.Version)}
	}
	var srcFields map[string]json.RawMessage
	if json.Unmarshal(v.Meta, &srcFields) == nil {
		for _, k := range []string{"readme", "readmeFilename", "gitHead"} {
			var s string
			if _, has := m[k]; !has && json.Unmarshal(srcFields[k], &s) == nil && s != "" {
				m[k] = s
			}
		}
	}
	file := p.Name + "-" + v.Version + ".tgz"
	m["_id"] = p.Name + "@" + v.Version
	m["dist"] = map[string]any{
		"integrity": "sha512-" + base64.StdEncoding.EncodeToString(f.Sums["sha512"]),
		"shasum":    hex.EncodeToString(f.Sums["sha1"]),
		"tarball":   npmURL(dst.Base, p.Name, "-", file),
	}
	mj, err := json.Marshal(m)
	if err != nil {
		return err
	}
	name, _ := json.Marshal(p.Name)
	tagj, _ := json.Marshal(tag)
	verj, _ := json.Marshal(v.Version)
	filej, _ := json.Marshal(file)
	prefix := []byte(`{"_id":` + string(name) + `,"name":` + string(name) + `,"dist-tags":{` + string(tagj) + `:` + string(verj) + `},"versions":{` +
		string(verj) + `:` + string(mj) + `},"access":null,"_attachments":{` + string(filej) +
		`:{"content_type":"application/octet-stream","length":` + strconv.FormatInt(f.Size, 10) + `,"data":"`)
	suffix := []byte(`"}}}`)
	total := int64(len(prefix)) + int64(base64.StdEncoding.EncodedLen(int(f.Size))) + int64(len(suffix)) //nolint:gosec // G115: a file is at most MaxBytes
	resp, err := dst.Do(ctx, Request{
		Method: http.MethodPut, URL: npmURL(dst.Base, p.Name),
		Header: http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Npm-Command": {"publish"}},
		Body: func() (io.Reader, int64, error) {
			return io.MultiReader(bytes.NewReader(prefix), newB64Reader(io.NewSectionReader(f.File, 0, f.Size)), bytes.NewReader(suffix)), total, nil
		},
	})
	if err != nil {
		return err
	}
	if resp.Status/100 != 2 {
		return refusal(resp)
	}
	return nil
}

// Finish implements Format: the dist-tags of the source, for the versions the target holds. latest is set last.
func (NPM) Finish(ctx context.Context, dst *TargetClient, p *Plan, held map[string]Held, tags map[string]string) ([]string, error) {
	var order []string
	for t := range p.Tags {
		order = append(order, t)
	}
	sort.Slice(order, func(i, j int) bool {
		if (order[i] == "latest") != (order[j] == "latest") {
			return order[j] == "latest"
		}
		return order[i] < order[j]
	})
	var notes []string
	var failed []string
	for _, t := range order {
		ver := p.Tags[t]
		if _, ok := held[ver]; !ok {
			notes = append(notes, "dist-tag "+t+" points to "+ver+", which the target does not hold")
			continue
		}
		if tags[t] == ver {
			continue
		}
		body, _ := json.Marshal(ver)
		resp, err := dst.Do(ctx, Request{
			Method: http.MethodPut, URL: strings.TrimSuffix(dst.Base.String(), "/") + "/-/package/" + strings.ReplaceAll(p.Name, "/", "%2f") + "/dist-tags/" + url.PathEscape(t),
			Header: http.Header{"Content-Type": {"application/json"}},
			Body:   func() (io.Reader, int64, error) { return bytes.NewReader(body), int64(len(body)), nil },
		})
		if err != nil {
			return notes, err
		}
		if resp.Status/100 != 2 {
			failed = append(failed, t+" ("+strconv.Itoa(resp.Status)+")")
			if fe := fatalOf(refusal(resp)); fe != nil {
				return notes, refusal(resp)
			}
			continue
		}
		tags[t] = ver
	}
	if len(failed) > 0 {
		return notes, fmt.Errorf("the target refused the dist-tags %s", strings.Join(failed, ", "))
	}
	return notes, nil
}
