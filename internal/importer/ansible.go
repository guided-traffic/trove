package importer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Ansible is the Ansible Galaxy format of the importer (R-ANS-027, R-IMP-009): it reads the collections of a source through
// the v3 documents of a Galaxy and publishes through the target's `v3/artifacts/collections/` as `ansible-galaxy collection
// publish` does. Roles are not imported; the report says how many the source holds.
type Ansible struct {
	mu        sync.Mutex
	rolesDone bool
}

// ID implements Format.
func (*Ansible) ID() string { return "ansible" }

var ansiblePart = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// splitName splits `<ns>.<name>` after folding it to lower case; ok is false for a name outside the grammar of R-ANS-002.
func splitName(name string) (ns, coll string, ok bool) {
	ns, coll, ok = strings.Cut(strings.ToLower(name), ".")
	if !ok || !ansiblePart.MatchString(ns) || !ansiblePart.MatchString(coll) {
		return "", "", false
	}
	for _, p := range []string{ns, coll} {
		if p[0] == '_' || !strings.ContainsFunc(p, unicode.IsLetter) {
			return "", "", false
		}
	}
	return ns, coll, true
}

// ValidName implements Format.
func (*Ansible) ValidName(name string) bool {
	_, _, ok := splitName(name)
	return ok
}

// galaxyPage is a paginated answer of the v3 API.
type galaxyPage struct {
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
	Data []json.RawMessage `json:"data"`
}

const (
	galaxyPageLimit = 100
	galaxyMaxPages  = 10000
	galaxyDocLimit  = 32 << 20
)

// resolve turns a link of an answer, absolute or relative, into an address.
func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// walk reads a paginated list from first on, following links.next, and hands each page to each. A 404 on the first page is
// found false.
func walk(first string, get func(u string, into *galaxyPage) (bool, error), each func(data []json.RawMessage) error) (bool, error) {
	u := first
	for n := 0; n < galaxyMaxPages; n++ {
		var pg galaxyPage
		found, err := get(u, &pg)
		if err != nil {
			return n > 0, err
		}
		if !found {
			if n == 0 {
				return false, nil
			}
			return true, fmt.Errorf("%s: the list ended with a missing page", redact(u))
		}
		if err := each(pg.Data); err != nil {
			return true, err
		}
		if pg.Links.Next == nil || *pg.Links.Next == "" || len(pg.Data) == 0 {
			return true, nil
		}
		if u, err = resolve(u, *pg.Links.Next); err != nil {
			return true, err
		}
	}
	return true, fmt.Errorf("the list has more than %d pages", galaxyMaxPages)
}

func srcPages(ctx context.Context, src *SourceClient) func(string, *galaxyPage) (bool, error) {
	return func(u string, into *galaxyPage) (bool, error) {
		return src.GetJSON(ctx, u, "application/json", galaxyDocLimit, into)
	}
}

func dstGet(ctx context.Context, dst *TargetClient, u string, into any) (bool, error) {
	resp, err := dst.Do(ctx, Request{Method: http.MethodGet, URL: u, Header: http.Header{"Accept": {"application/json"}}})
	if err != nil {
		return false, err
	}
	switch {
	case resp.Status == http.StatusNotFound:
		return false, nil
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return false, &Refusal{Status: resp.Status, Code: galaxyCode(resp)}
	case resp.Status != http.StatusOK:
		return false, fmt.Errorf("%s answered %d", redact(u), resp.Status)
	}
	if err := json.Unmarshal(resp.Body, into); err != nil {
		return false, fmt.Errorf("%s: the answer is not a Galaxy document: %w", redact(u), err)
	}
	return true, nil
}

// galaxyCode reads the code of the first error of a Galaxy-shaped error body.
func galaxyCode(resp *Response) string {
	var b struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(resp.Body, &b) == nil && len(b.Errors) > 0 {
		return b.Errors[0].Code
	}
	return ""
}

func galaxyRefusal(resp *Response) *Refusal {
	r := &Refusal{Status: resp.Status, Code: galaxyCode(resp)}
	var b struct {
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if json.Unmarshal(resp.Body, &b) == nil && len(b.Errors) > 0 {
		r.Msg = b.Errors[0].Detail
	}
	return r
}

// Preflight implements Format: the discovery document of the target is a trove one and lists v3.
func (a *Ansible) Preflight(ctx context.Context, dst *TargetClient) error {
	var doc struct {
		Versions    map[string]string `json:"available_versions"`
		Description string            `json:"description"`
	}
	found, err := dstGet(ctx, dst, dst.Base.String(), &doc)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("the target answered 404: is the address an Ansible repository of trove that the token can read?")
	}
	if !strings.HasPrefix(doc.Description, "trove") {
		return fmt.Errorf("the target is not a trove Ansible repository: its discovery document does not say so")
	}
	if _, ok := doc.Versions["v3"]; !ok {
		return fmt.Errorf("the target lists no v3 API")
	}
	return nil
}

func apiRoot(u *url.URL) string { return ensureSlash(u.String()) }

// Search implements Format with the collection index of the source; q filters on `<ns>.<name>`.
func (*Ansible) Search(ctx context.Context, src *SourceClient, q string) ([]string, error) {
	q = strings.ToLower(q)
	var names []string
	first := apiRoot(src.Origin) + "v3/collections/?limit=" + strconv.Itoa(galaxyPageLimit) + "&offset=0"
	found, err := walk(first, srcPages(ctx, src), func(data []json.RawMessage) error {
		for _, raw := range data {
			var c struct {
				Namespace json.RawMessage `json:"namespace"`
				Name      string          `json:"name"`
			}
			if err := json.Unmarshal(raw, &c); err != nil {
				return fmt.Errorf("the collection index lists an entry that is not a collection: %w", err)
			}
			ns := namespaceName(c.Namespace)
			if ns == "" || c.Name == "" {
				continue
			}
			if full := ns + "." + c.Name; strings.Contains(strings.ToLower(full), q) {
				names = append(names, full)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the source has no collection index at %sv3/collections/; is --from the API root of a Galaxy?", apiRoot(src.Origin))
	}
	return names, nil
}

// namespaceName reads a namespace given as a string or as an object with a name.
func namespaceName(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Name
	}
	return ""
}

type galaxyVersionDoc struct {
	Version     string `json:"version"`
	CreatedAt   string `json:"created_at"`
	DownloadURL string `json:"download_url"`
	Artifact    struct {
		SHA256 string `json:"sha256"`
	} `json:"artifact"`
}

type galaxyListEntry struct {
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
}

func collectionURL(root, ns, name string) string {
	return root + "v3/collections/" + url.PathEscape(ns) + "/" + url.PathEscape(name) + "/"
}

func versionsURL(root, ns, name string) string {
	return collectionURL(root, ns, name) + "versions/"
}

func sha256Of(hexSum string) []byte {
	sum, err := hex.DecodeString(strings.TrimSpace(hexSum))
	if err != nil || len(sum) != 32 {
		return nil
	}
	return sum
}

// Plan implements Format: the version list of the source and, per version, the version document with the download address
// and the announced digest.
func (a *Ansible) Plan(ctx context.Context, src *SourceClient, name string) (*Plan, error) {
	ns, coll, ok := splitName(name)
	if !ok {
		return nil, fmt.Errorf("%q is not a collection name", name)
	}
	root := apiRoot(src.Origin)
	p := &Plan{Name: ns + "." + coll}
	var listed []galaxyListEntry
	found, err := walk(versionsURL(root, ns, coll)+"?limit="+strconv.Itoa(galaxyPageLimit), srcPages(ctx, src), func(data []json.RawMessage) error {
		for _, raw := range data {
			var e galaxyListEntry
			if err := json.Unmarshal(raw, &e); err != nil || e.Version == "" {
				return fmt.Errorf("the version list of %s lists an entry without a version", p.Name)
			}
			listed = append(listed, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return p, nil
	}
	seen := map[string]bool{}
	for _, e := range listed {
		if seen[e.Version] {
			continue
		}
		seen[e.Version] = true
		var doc galaxyVersionDoc
		ok, err := src.GetJSON(ctx, versionsURL(root, ns, coll)+url.PathEscape(e.Version)+"/", "application/json", galaxyDocLimit, &doc)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("the source lists %s %s and has no document for it", p.Name, e.Version)
		}
		v := Version{Version: e.Version, URL: doc.DownloadURL}
		if sum := sha256Of(doc.Artifact.SHA256); sum != nil {
			v.Want = Digest{Alg: "sha256", Sum: sum}
		}
		created := doc.CreatedAt
		if created == "" {
			created = e.CreatedAt
		}
		if t, err := time.Parse(time.RFC3339, created); err == nil && t.Year() > 1900 {
			v.Published = t
		}
		v.Listed = true
		p.Versions = append(p.Versions, v)
	}
	sortVersions(p.Versions)
	a.notKept(ctx, src, root, ns, coll, p)
	return p, nil
}

// notKept notes what the target cannot hold: the deprecation flag of the collection and, once per run, the roles of the
// source. Both are best effort; a source that does not answer is not a failure of the copy.
func (a *Ansible) notKept(ctx context.Context, src *SourceClient, root, ns, coll string, p *Plan) {
	var c struct {
		Deprecated bool `json:"deprecated"`
	}
	if ok, err := src.GetJSON(ctx, collectionURL(root, ns, coll), "application/json", galaxyDocLimit, &c); err == nil && ok && c.Deprecated {
		p.NotKept = append(p.NotKept, "deprecation flag of the collection (trove stores none for a local collection)")
	}
	a.mu.Lock()
	done := a.rolesDone
	a.rolesDone = true
	a.mu.Unlock()
	if done {
		return
	}
	var roles struct {
		Count int `json:"count"`
	}
	if ok, err := src.GetJSON(ctx, root+"v1/roles/?page_size=1", "application/json", galaxyDocLimit, &roles); err == nil && ok && roles.Count > 0 {
		p.NotKept = append(p.NotKept, strconv.Itoa(roles.Count)+" roles of the source (roles cannot be published to a local repository; reported with the first collection of the run)")
	}
}

// Held implements Format: the version list of the target and the digest of each version.
func (*Ansible) Held(ctx context.Context, dst *TargetClient, name string) (map[string]Held, map[string]string, error) {
	ns, coll, ok := splitName(name)
	if !ok {
		return nil, nil, fmt.Errorf("%q is not a collection name", name)
	}
	root := apiRoot(dst.Base)
	held := map[string]Held{}
	var versions []string
	_, err := walk(versionsURL(root, ns, coll)+"?limit="+strconv.Itoa(galaxyPageLimit), func(u string, into *galaxyPage) (bool, error) {
		return dstGet(ctx, dst, u, into)
	}, func(data []json.RawMessage) error {
		for _, raw := range data {
			var e galaxyListEntry
			if json.Unmarshal(raw, &e) == nil && e.Version != "" {
				versions = append(versions, e.Version)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, ver := range versions {
		var doc galaxyVersionDoc
		ok, err := dstGet(ctx, dst, versionsURL(root, ns, coll)+url.PathEscape(ver)+"/", &doc)
		if err != nil {
			return nil, nil, err
		}
		h := Held{Digests: map[string][]byte{}, Listed: true}
		if ok {
			if sum := sha256Of(doc.Artifact.SHA256); sum != nil {
				h.Digests["sha256"] = sum
			}
		}
		held[ver] = h
	}
	return held, nil, nil
}

// pollTask waits for the import task of a publish to finish. The task of trove is final when it is created, so one read
// is the rule; a 404 is tried again a few times, as the client does.
func pollTask(ctx context.Context, dst *TargetClient, taskURL string) error {
	const attempts = 30
	for i := 0; i < attempts; i++ {
		var task struct {
			State      string          `json:"state"`
			FinishedAt json.RawMessage `json:"finished_at"`
			Error      *struct {
				Code        string `json:"code"`
				Description string `json:"description"`
			} `json:"error"`
		}
		found, err := dstGet(ctx, dst, taskURL, &task)
		if err != nil {
			return err
		}
		finished := len(bytes.TrimSpace(task.FinishedAt)) > 0 && string(bytes.TrimSpace(task.FinishedAt)) != "null"
		switch {
		case found && task.State == "failed":
			r := &Refusal{Status: http.StatusUnprocessableEntity, Code: "import_failed"}
			if task.Error != nil {
				r.Code, r.Msg = task.Error.Code, task.Error.Description
			}
			return r
		case found && finished && task.State == "completed":
			return nil
		}
		if err := dst.Retry.sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return fmt.Errorf("the import task %s did not finish", redact(taskURL))
}

// Publish implements Format: the multipart publish of `ansible-galaxy collection publish`, then the task.
func (*Ansible) Publish(ctx context.Context, dst *TargetClient, p *Plan, v Version, f *File, _ string) error {
	ns, coll, ok := splitName(p.Name)
	if !ok {
		return fmt.Errorf("%q is not a collection name", p.Name)
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
	if err := mw.WriteField("sha256", hex.EncodeToString(f.Sums["sha256"])); err != nil {
		return err
	}
	if _, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="file"; filename="` + ns + "-" + coll + "-" + v.Version + `.tar.gz"`},
		"Content-Type":        {"application/octet-stream"},
	}); err != nil {
		return err
	}
	tail := []byte("\r\n--" + boundary + "--\r\n")
	total := int64(head.Len()) + f.Size + int64(len(tail))
	resp, err := dst.Do(ctx, Request{
		Method: http.MethodPost, URL: apiRoot(dst.Base) + "v3/artifacts/collections/",
		Header: http.Header{"Content-Type": {mw.FormDataContentType()}, "Accept": {"application/json"}},
		Body: func() (io.Reader, int64, error) {
			return io.MultiReader(bytes.NewReader(head.Bytes()), io.NewSectionReader(f.File, 0, f.Size), bytes.NewReader(tail)), total, nil
		},
	})
	if err != nil {
		return err
	}
	if resp.Status/100 != 2 {
		return galaxyRefusal(resp)
	}
	var ans struct {
		Task string `json:"task"`
	}
	if json.Unmarshal(resp.Body, &ans) != nil || ans.Task == "" {
		return fmt.Errorf("the target accepted the publish and named no import task")
	}
	taskURL, err := resolve(apiRoot(dst.Base), ans.Task)
	if err != nil {
		return err
	}
	return pollTask(ctx, dst, taskURL)
}

// Finish implements Format: a collection version has no state beyond its publish.
func (*Ansible) Finish(context.Context, *TargetClient, *Plan, map[string]Held, map[string]string) ([]string, error) {
	return nil, nil
}
