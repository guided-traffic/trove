package importer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NuGet is the NuGet format of the importer (R-IMP-005, R-IMP-009): it reads the source through the service index, the
// registration and the package content, and pushes through the package publish resource as `dotnet nuget push` does.
type NuGet struct {
	mu        sync.Mutex
	src, dst  *serviceIndex
	unlistOK  map[string]bool
	unlistWhy map[string]string
}

var nugetID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// ID implements Format.
func (*NuGet) ID() string { return "nuget" }

// ValidName implements Format.
func (*NuGet) ValidName(name string) bool { return nugetID.MatchString(name) }

type serviceIndex struct {
	resources []resource
}

type resource struct {
	ID   string `json:"@id"`
	Type string `json:"@type"`
}

func (s *serviceIndex) find(prefix string, exact ...string) string {
	for _, e := range exact {
		for _, r := range s.resources {
			if r.Type == e {
				return r.ID
			}
		}
	}
	if prefix != "" {
		for _, r := range s.resources {
			if strings.HasPrefix(r.Type, prefix) {
				return r.ID
			}
		}
	}
	return ""
}

func parseIndex(b []byte) (*serviceIndex, error) {
	var doc struct {
		Resources []resource `json:"resources"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || len(doc.Resources) == 0 {
		return nil, fmt.Errorf("the address does not answer with a NuGet service index")
	}
	return &serviceIndex{resources: doc.Resources}, nil
}

func (n *NuGet) sourceIndex(ctx context.Context, src *SourceClient) (*serviceIndex, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.src != nil {
		return n.src, nil
	}
	status, body, err := src.GetStatus(ctx, src.Origin.String(), "application/json", 8<<20)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("the service index answered %d", status)
	}
	idx, err := parseIndex(body)
	if err != nil {
		return nil, err
	}
	n.src = idx
	return idx, nil
}

func (n *NuGet) targetIndex(ctx context.Context, dst *TargetClient) (*serviceIndex, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.dst != nil {
		return n.dst, nil
	}
	resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: dst.Base.String(), Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, &Refusal{Status: resp.Status, Code: "unauthorized", Msg: "the token is not accepted"}
	default:
		return nil, fmt.Errorf("the service index of the target answered %d: is the address a NuGet repository of trove that the token can read?", resp.Status)
	}
	idx, err := parseIndex(resp.Body)
	if err != nil {
		return nil, err
	}
	n.dst = idx
	return idx, nil
}

func ensureSlash(s string) string {
	if strings.HasSuffix(s, "/") {
		return s
	}
	return s + "/"
}

// Preflight implements Format: the service index of the target lists the publish resource.
func (n *NuGet) Preflight(ctx context.Context, dst *TargetClient) error {
	idx, err := n.targetIndex(ctx, dst)
	if err != nil {
		return err
	}
	if idx.find("", "PackagePublish/2.0.0") == "" {
		return fmt.Errorf("the target lists no package publish resource: it is not a local repository, or the token may not publish to it")
	}
	return nil
}

// Search implements Format with the search resource of the service index.
func (n *NuGet) Search(ctx context.Context, src *SourceClient, q string) ([]string, error) {
	idx, err := n.sourceIndex(ctx, src)
	if err != nil {
		return nil, err
	}
	base := idx.find("SearchQueryService")
	if base == "" {
		return nil, fmt.Errorf("the source lists no search resource; give the package IDs")
	}
	var names []string
	const take = 100
	for skip, pages := 0, 0; pages < 1000; pages++ {
		var res struct {
			TotalHits int `json:"totalHits"`
			Data      []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		u := base + "?q=" + url.QueryEscape(q) + "&skip=" + strconv.Itoa(skip) + "&take=" + strconv.Itoa(take) + "&prerelease=true&semVerLevel=2.0.1"
		found, err := src.GetJSON(ctx, u, "application/json", 32<<20, &res)
		if err != nil {
			return nil, err
		}
		if !found || len(res.Data) == 0 {
			break
		}
		for _, d := range res.Data {
			names = append(names, d.ID)
		}
		skip += len(res.Data)
		if skip >= res.TotalHits {
			break
		}
	}
	return names, nil
}

type regEntry struct {
	ID                   string          `json:"id"`
	Version              string          `json:"version"`
	Listed               *bool           `json:"listed"`
	Published            string          `json:"published"`
	PackageContent       string          `json:"packageContent"`
	PackageHash          string          `json:"packageHash"`
	PackageHashAlgorithm string          `json:"packageHashAlgorithm"`
	Deprecation          json.RawMessage `json:"deprecation"`
}

type regLeaf struct {
	CatalogEntry   json.RawMessage `json:"catalogEntry"`
	PackageContent string          `json:"packageContent"`
}

type regPage struct {
	ID    string    `json:"@id"`
	Items []regLeaf `json:"items"`
}

type regIndex struct {
	Items []regPage `json:"items"`
}

type fetchFunc func(ctx context.Context, rawURL string) (status int, body []byte, err error)

// readRegistration reads every version of a package from a registration resource: the index, then each page that is not
// inline. It returns found false for a package the registration does not know.
func readRegistration(ctx context.Context, fetch fetchFunc, base, lowerID string) (entries []regEntry, found bool, err error) {
	get := func(u string, into any) (bool, error) {
		status, body, err := fetch(ctx, u)
		if err != nil {
			return false, err
		}
		if status == http.StatusNotFound {
			return false, nil
		}
		if status != http.StatusOK {
			return false, fmt.Errorf("%s answered %d", redact(u), status)
		}
		if body, err = maybeGunzip(body, 256<<20); err != nil {
			return false, err
		}
		if err := json.Unmarshal(body, into); err != nil {
			return false, fmt.Errorf("%s: not a registration document: %w", redact(u), err)
		}
		return true, nil
	}
	var idx regIndex
	ok, err := get(ensureSlash(base)+lowerID+"/index.json", &idx)
	if err != nil || !ok {
		return nil, false, err
	}
	for _, pg := range idx.Items {
		items := pg.Items
		if items == nil {
			var full regPage
			if ok, err := get(pg.ID, &full); err != nil {
				return nil, true, err
			} else if !ok {
				continue
			}
			items = full.Items
		}
		for _, leaf := range items {
			e, err := leafEntry(ctx, get, leaf)
			if err != nil {
				return nil, true, err
			}
			entries = append(entries, e)
		}
	}
	return entries, true, nil
}

func leafEntry(_ context.Context, get func(string, any) (bool, error), leaf regLeaf) (regEntry, error) {
	var e regEntry
	raw := bytes.TrimSpace(leaf.CatalogEntry)
	switch {
	case len(raw) > 0 && raw[0] == '{':
		if err := json.Unmarshal(raw, &e); err != nil {
			return e, err
		}
	case len(raw) > 0 && raw[0] == '"':
		var u string
		if err := json.Unmarshal(raw, &u); err != nil {
			return e, err
		}
		if _, err := get(u, &e); err != nil {
			return e, err
		}
	}
	if e.PackageContent == "" {
		e.PackageContent = leaf.PackageContent
	}
	return e, nil
}

// Plan implements Format.
func (n *NuGet) Plan(ctx context.Context, src *SourceClient, name string) (*Plan, error) {
	idx, err := n.sourceIndex(ctx, src)
	if err != nil {
		return nil, err
	}
	base := idx.find("", "RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl")
	if base == "" {
		return nil, fmt.Errorf("the source lists no registration resource")
	}
	lower := strings.ToLower(name)
	entries, _, err := readRegistration(ctx, func(ctx context.Context, u string) (int, []byte, error) {
		return src.GetStatus(ctx, u, "application/json", 64<<20)
	}, base, lower)
	if err != nil {
		return nil, err
	}
	p := &Plan{Name: name}
	var deprecated []string
	for _, e := range entries {
		v := Version{Version: strings.ToLower(e.Version), URL: e.PackageContent, Listed: e.Listed == nil || *e.Listed}
		if !strings.EqualFold(e.ID, name) || v.Version == "" {
			return nil, fmt.Errorf("the registration of %s lists %q %q", name, e.ID, e.Version)
		}
		if t, err := time.Parse(time.RFC3339, e.Published); err == nil && t.Year() > 1900 {
			v.Published = t
		}
		if v.URL == "" {
			if base := idx.find("", "PackageBaseAddress/3.0.0"); base != "" {
				v.URL = ensureSlash(base) + lower + "/" + v.Version + "/" + lower + "." + v.Version + ".nupkg"
			}
		}
		if sum, err := base64.StdEncoding.DecodeString(e.PackageHash); err == nil {
			if alg := strings.ToLower(e.PackageHashAlgorithm); algSize(alg) == len(sum) && len(sum) > 0 {
				v.Want = Digest{Alg: alg, Sum: sum}
			}
		}
		if d := bytes.TrimSpace(e.Deprecation); len(d) > 0 && string(d) != "null" {
			deprecated = append(deprecated, v.Version)
		}
		p.Versions = append(p.Versions, v)
	}
	sortVersions(p.Versions)
	if len(deprecated) > 0 {
		sort.Strings(deprecated)
		p.NotKept = append(p.NotKept, "deprecation of "+strings.Join(deprecated, ", ")+" (trove stores no deprecation of a local NuGet package)")
	}
	return p, nil
}

// Held implements Format: the registration of the target.
func (n *NuGet) Held(ctx context.Context, dst *TargetClient, name string) (map[string]Held, map[string]string, error) {
	idx, err := n.targetIndex(ctx, dst)
	if err != nil {
		return nil, nil, err
	}
	base := idx.find("", "RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl")
	if base == "" {
		return nil, nil, fmt.Errorf("the target lists no registration resource")
	}
	entries, _, err := readRegistration(ctx, func(ctx context.Context, u string) (int, []byte, error) {
		resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: u, Header: http.Header{"Accept": {"application/json"}}})
		if err != nil {
			return 0, nil, err
		}
		return resp.Status, resp.Body, nil
	}, base, strings.ToLower(name))
	if err != nil {
		return nil, nil, err
	}
	held := map[string]Held{}
	for _, e := range entries {
		ver := strings.ToLower(e.Version)
		h := Held{Digests: map[string][]byte{}, Listed: e.Listed == nil || *e.Listed}
		// A local repository's registration carries no package hash; the stored file's digests are in the headers of a HEAD
		// of the package content, which the target answers from its database (R-STOR-012).
		if e.PackageContent != "" {
			resp, err := dst.Do(ctx, Request{Method: http.MethodHead, URL: e.PackageContent, Header: http.Header{"Want-Repr-Digest": {"sha-512=10, sha-256=5"}}})
			if err != nil {
				return nil, nil, err
			}
			if resp.Status == http.StatusOK {
				h.Digests = parseReprDigest(resp.Header.Get("Repr-Digest"))
			}
		}
		held[ver] = h
	}
	return held, nil, nil
}

// parseReprDigest reads the Repr-Digest field of RFC 9530: `sha-512=:<base64>:, sha-256=:<base64>:`.
func parseReprDigest(v string) map[string][]byte {
	out := map[string][]byte{}
	for _, item := range strings.Split(v, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || !strings.HasPrefix(val, ":") || !strings.HasSuffix(val, ":") || len(val) < 2 {
			continue
		}
		sum, err := base64.StdEncoding.DecodeString(strings.Trim(val, ":"))
		if err != nil {
			continue
		}
		if alg := strings.ReplaceAll(strings.ToLower(k), "-", ""); algSize(alg) == len(sum) {
			out[alg] = sum
		}
	}
	return out
}

// Publish implements Format: PUT of a multipart body whose first part is the package, to the publish resource.
func (n *NuGet) Publish(ctx context.Context, dst *TargetClient, _ *Plan, _ Version, f *File, _ string) error {
	idx, err := n.targetIndex(ctx, dst)
	if err != nil {
		return err
	}
	pub := idx.find("", "PackagePublish/2.0.0")
	if pub == "" {
		return fmt.Errorf("the target lists no package publish resource")
	}
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	boundary := "trove-import-" + hex.EncodeToString(rnd[:])
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	if _, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="package"; filename="package.nupkg"`},
		"Content-Type":        {"application/octet-stream"},
	}); err != nil {
		return err
	}
	tail := []byte("\r\n--" + boundary + "--\r\n")
	total := int64(head.Len()) + f.Size + int64(len(tail))
	resp, err := dst.Do(ctx, Request{
		Method: http.MethodPut, URL: pub,
		Header: http.Header{"Content-Type": {mw.FormDataContentType()}},
		Body: func() (io.Reader, int64, error) {
			return io.MultiReader(bytes.NewReader(head.Bytes()), io.NewSectionReader(f.File, 0, f.Size), bytes.NewReader(tail)), total, nil
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

// canUnlist decides whether the protocol DELETE of the target only unlists. A repository with deleteMode delete would remove
// the version for good and write its tombstone (R-NUGET-007), so the importer replays an unlisted state only after it has read
// `nuget.deleteMode: unlist` of the repository's document, which takes the right to read the configuration.
func (n *NuGet) canUnlist(ctx context.Context, dst *TargetClient) (bool, string) {
	key := dst.Base.String()
	n.mu.Lock()
	ok, seen := n.unlistOK[key]
	why := n.unlistWhy[key]
	n.mu.Unlock()
	if seen {
		return ok, why
	}
	ok, why = n.readDeleteMode(ctx, dst)
	n.mu.Lock()
	if n.unlistOK == nil {
		n.unlistOK, n.unlistWhy = map[string]bool{}, map[string]string{}
	}
	n.unlistOK[key], n.unlistWhy[key] = ok, why
	n.mu.Unlock()
	return ok, why
}

var feedPath = regexp.MustCompile(`^(.*)/api/nuget/([^/]+)/([^/]+)/`)

func (n *NuGet) readDeleteMode(ctx context.Context, dst *TargetClient) (bool, string) {
	m := feedPath.FindStringSubmatch(dst.Base.Path)
	if m == nil {
		return false, "the repository address is not in the form /api/nuget/<project>/<repository>/"
	}
	u := *dst.Base
	u.Path, u.RawPath, u.RawQuery = m[1]+"/api/v1/projects/"+url.PathEscape(m[2])+"/repositories/"+url.PathEscape(m[3]), "", ""
	resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: u.String(), Header: http.Header{"Accept": {"application/json"}}})
	if err != nil || resp.Status != http.StatusOK {
		return false, "the repository's configuration could not be read, so it is not known that a delete only unlists"
	}
	// The consumer view of a caller without the right to read the configuration has an ETag that ends in -consumer and no
	// settings; only the full document says what the repository does on a DELETE (R-REPO-010).
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" || strings.HasSuffix(etag, "-consumer") {
		return false, "the token may not read the repository's configuration, so it is not known that a delete only unlists"
	}
	var doc struct {
		NuGet struct {
			DeleteMode string `json:"deleteMode"`
		} `json:"nuget"`
	}
	if json.Unmarshal(resp.Body, &doc) != nil {
		return false, "the repository's configuration could not be read, so it is not known that a delete only unlists"
	}
	if mode := doc.NuGet.DeleteMode; mode != "" && mode != "unlist" {
		return false, "the repository's deleteMode is " + mode + ": a delete would remove the version for good"
	}
	return true, ""
}

// Finish implements Format: the versions the source lists as unlisted are unlisted in the target.
func (n *NuGet) Finish(ctx context.Context, dst *TargetClient, p *Plan, held map[string]Held, _ map[string]string) ([]string, error) {
	var todo []string
	for _, v := range p.Versions {
		if h, ok := held[v.Version]; ok && !v.Listed && h.Listed {
			todo = append(todo, v.Version)
		}
	}
	if len(todo) == 0 {
		return nil, nil
	}
	if ok, why := n.canUnlist(ctx, dst); !ok {
		return []string{"unlisted state of " + strings.Join(todo, ", ") + " not kept: " + why}, nil
	}
	idx, err := n.targetIndex(ctx, dst)
	if err != nil {
		return nil, err
	}
	pub := idx.find("", "PackagePublish/2.0.0")
	var failed []string
	for _, ver := range todo {
		resp, err := dst.Do(ctx, Request{Method: http.MethodDelete, URL: ensureSlash(pub) + url.PathEscape(strings.ToLower(p.Name)) + "/" + url.PathEscape(ver)})
		if err != nil {
			return nil, err
		}
		if resp.Status/100 != 2 {
			failed = append(failed, ver+" ("+strconv.Itoa(resp.Status)+")")
		}
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("the target refused to unlist %s", strings.Join(failed, ", "))
	}
	return nil, nil
}
