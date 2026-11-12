package v1

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/api/edge"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/ansible/docgen"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/packages/readme"
	"github.com/guided-traffic/trove/internal/packages/view"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The documentation operations of the screens (R-ANS-036, R-UI-060 to R-UI-064, Q-API-260 to Q-API-269): the tree, one entry and one file
// of the documentation of a package version. Each is declared wiki content in the document (x-trove-part: wiki, [Operation.Part]) and
// reads rows only, never an upstream.

// The bounds of the operations (R-ANS-036).
const (
	maxDocDepth      = 12
	maxQueryTerms    = rows.MaxDocSearchTerms
	maxQueryTermRune = rows.MaxDocSearchTermLength
)

// docsRead is a documentation read resolved to one repository.
type docsRead struct {
	ps    PackageServices
	st    AnsibleStore
	ctx   context.Context //nolint:containedctx // the request's context, carrying the holder of its index
	name  string
	ver   string
	repo  repository.Repository
	state rows.DocState
}

func docsNotFound() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "The version does not exist.")
}

// resolveDocs finds the repository a documentation read is answered from (R-ANS-036): the one named by repository, a virtual one resolved to the
// first of its members, in the order of the tiers, that holds the version and whose documentation is not none; or, without repository, the
// first repository that holds the version, local before remote, with a state other than none. A version the caller cannot see, a repository
// that does not hold it, a format other than Ansible Galaxy and a repository whose wiki the caller may not read are all the same 404.
func (s *Server) resolveDocs(r *http.Request, id gen.PackageId, version gen.PackageVersionPath, repoParam *gen.DocRepository) (*docsRead, error) {
	ps, err := s.packageServices()
	if err != nil {
		return nil, err
	}
	st := s.o.Ansible
	if st == nil {
		return nil, unavailable("The Ansible Galaxy documentation")
	}
	format, name, ok := view.ParseID(id)
	if !ok || format != "ansible" {
		return nil, docsNotFound()
	}
	ctx, sc, err := s.packageScope(r, ps, view.ScopeRequest{Formats: []string{format}, ExactName: name})
	if err != nil {
		return nil, err
	}
	d, found, err := ps.View.Version(ctx, sc, format, name, version)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, docsNotFound()
	}
	held := map[qname.Repo]string{}
	for _, e := range d.Repositories {
		held[e.Repo] = e.Class
	}
	cands, err := docsCandidates(ctx, ps, held, repoParam)
	if err != nil {
		return nil, err
	}
	var first *repository.Repository
	for i := range cands {
		c := &cands[i]
		// The wiki of the repository the documentation would come from (R-ANS-037, R-API-058): the caller's read scope decides, without an audit event.
		if !sc.MayReadWiki(c.Name, name, version) {
			continue
		}
		state, err := st.DocState(ctx, c.NumericID(), name, version)
		if err != nil {
			return nil, err
		}
		if first == nil {
			first = c
		}
		if state.State != rows.DocNone {
			return &docsRead{ps: ps, st: st, ctx: ctx, name: name, ver: version, repo: *c, state: state}, nil
		}
	}
	if first == nil {
		return nil, docsNotFound()
	}
	return &docsRead{ps: ps, st: st, ctx: ctx, name: name, ver: version, repo: *first, state: rows.DocState{State: rows.DocNone}}, nil
}

// docsCandidates lists the physical repositories a read may come from, in the order they are tried.
func docsCandidates(ctx context.Context, ps PackageServices, held map[qname.Repo]string, repoParam *gen.DocRepository) ([]repository.Repository, error) {
	byTier := func(rs []repository.Repository) []repository.Repository {
		var local, remote []repository.Repository
		for _, m := range rs {
			switch m.Class {
			case repository.Local:
				local = append(local, m)
			case repository.Remote:
				remote = append(remote, m)
			}
		}
		return append(local, remote...)
	}
	physical := func(names []qname.Repo) ([]repository.Repository, error) {
		var rs []repository.Repository
		for _, n := range names {
			if held[n] == "" {
				continue
			}
			rep, err := ps.Reader.ByName(ctx, n)
			if err != nil {
				if errs.KindOf(err) == errs.NotFound {
					continue
				}
				return nil, err
			}
			rs = append(rs, rep)
		}
		return byTier(rs), nil
	}
	if repoParam == nil {
		names := make([]qname.Repo, 0, len(held))
		for n, class := range held {
			if class != string(repository.Virtual) {
				names = append(names, n)
			}
		}
		sort.Slice(names, func(i, j int) bool { return view.CompareNames(names[i], names[j]) < 0 })
		return physical(names)
	}
	project, key, ok := strings.Cut(*repoParam, "/")
	n := qname.Repo{Project: project, Key: key}
	if _, listed := held[n]; !ok || !listed {
		return nil, docsNotFound()
	}
	rep, err := ps.Reader.ByName(ctx, n)
	if err != nil {
		return nil, err
	}
	if rep.Class != repository.Virtual {
		return []repository.Repository{rep}, nil
	}
	members := rep.Members()
	names := make([]qname.Repo, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	return physical(names)
}

// repoName is the name the answer gives the repository: <projectKey>/<repoKey>.
func repoName(r repository.Repository) string { return r.Name.Project + "/" + r.Name.Key }

// GetPackageVersionDocs implements GET /api/v1/packages/{packageId}/versions/{version}/docs (R-ANS-036).
func (s *Server) GetPackageVersionDocs(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath, params gen.GetPackageVersionDocsParams) {
	d, err := s.resolveDocs(r, id, version, params.Repository)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	query, err := parseDocQuery(params.Q)
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	out := gen.PackageDocTree{State: gen.PackageDocTreeState(d.state.State), Repository: repoName(d.repo)}
	if d.state.State == rows.DocUnavailable && d.state.Reason != "" {
		reason := gen.PackageDocTreeReason(d.state.Reason)
		out.Reason = &reason
	}
	if d.state.State == rows.DocAvailable {
		tree, err := d.st.DocTree(d.ctx, d.repo.NumericID(), d.name, d.ver, query)
		if err != nil {
			httpserver.Problem(w, r, errs.Wrap(err, "api.getPackageVersionDocs"))
			return
		}
		out = treeOf(out, tree, query != nil, d.canSeeWarnings())
	}
	writeTagged(w, r, out)
}

// canSeeWarnings reports whether the caller holds deploy on the repository: the warnings describe the publisher's sources and are shown as the
// import task is (R-ANS-036).
func (d *docsRead) canSeeWarnings() bool {
	acc, ok := edge.AccessFrom(d.ctx)
	if !ok {
		return false
	}
	// Check audits nothing: a reader who is not shown the warnings is no refusal.
	res, err := acc.Check([]authz.CheckItem{{Repository: d.repo.Name, Action: authz.Deploy}})
	return err == nil && len(res) == 1 && res[0]
}

// parseDocQuery validates the search text (R-ANS-036): control characters are removed, a text of fewer than 2 characters or of more than
// 512 is 422 invalid_query, and the text is split on white space into at most 8 terms of at most 64 characters.
func parseDocQuery(q *string) (*rows.DocQuery, error) {
	if q == nil {
		return nil, nil
	}
	if len(*q) > 4*rows.MaxDocQueryLength {
		return nil, invalidQuery()
	}
	text := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, *q)
	text = strings.TrimSpace(text)
	if n := utf8.RuneCountInString(text); n < rows.MinDocQueryLength || n > rows.MaxDocQueryLength {
		return nil, invalidQuery()
	}
	terms := strings.Fields(text)
	if len(terms) > maxQueryTerms {
		terms = terms[:maxQueryTerms]
	}
	for i, t := range terms {
		if r := []rune(t); len(r) > maxQueryTermRune {
			terms[i] = string(r[:maxQueryTermRune])
		}
	}
	return &rows.DocQuery{Terms: terms}, nil
}

func invalidQuery() error {
	return errs.E(errs.Unprocessable, errs.CodeInvalidQuery, "The search text has at least 2 and at most 512 characters.")
}

// category is the group a content type belongs to (R-ANS-036).
func category(typ string) (gen.PackageDocGroupCategory, string) {
	switch typ {
	case "module":
		return gen.PackageDocGroupCategoryModule, "Modules"
	case "role":
		return gen.PackageDocGroupCategoryRole, "Roles"
	}
	return gen.PackageDocGroupCategoryPlugin, "Plugins"
}

// treeOf fills the answer of an available version from the rows: the groups in the order modules, plugins, roles, the types and entries in
// alphabetical order, and counts that are those before the search (R-ANS-036).
func treeOf(out gen.PackageDocTree, tree rows.DocTree, searched, warnings bool) gen.PackageDocTree {
	if tree.State.Source != "" {
		src := gen.PackageDocTreeSource(tree.State.Source)
		out.Source = &src
	}
	if !tree.State.BuiltAt.IsZero() {
		at := tree.State.BuiltAt
		out.BuiltAt = &at
	}
	truncated := tree.State.Truncated
	out.Truncated = &truncated
	if searched {
		n := int32(min(len(tree.Entries), 1<<30)) //nolint:gosec // at most 5,000
		out.MatchCount = &n
	}
	files := make([]gen.PackageDocFileHead, 0, len(tree.Files))
	for _, f := range tree.Files {
		files = append(files, gen.PackageDocFileHead{Name: f.Name, Title: f.Title})
	}
	out.Files = &files
	byType := map[string][]gen.PackageDocEntryHead{}
	for _, e := range tree.Entries {
		byType[e.Type] = append(byType[e.Type], gen.PackageDocEntryHead{Type: e.Type, Name: e.Name, ShortDescription: e.ShortDescription, HasDocs: e.HasDocs})
	}
	types := make([]string, 0, len(tree.Counts))
	for t := range tree.Counts {
		types = append(types, t)
	}
	sort.Strings(types)
	groups := []gen.PackageDocGroup{
		{Category: gen.PackageDocGroupCategoryModule, Label: "Modules", Types: []gen.PackageDocType{}},
		{Category: gen.PackageDocGroupCategoryPlugin, Label: "Plugins", Types: []gen.PackageDocType{}},
		{Category: gen.PackageDocGroupCategoryRole, Label: "Roles", Types: []gen.PackageDocType{}},
	}
	for _, t := range types {
		cat, _ := category(t)
		for i := range groups {
			if groups[i].Category != cat {
				continue
			}
			entries := byType[t]
			if entries == nil {
				entries = []gen.PackageDocEntryHead{}
			}
			n := int32(min(tree.Counts[t], 1<<30)) //nolint:gosec // at most 5,000
			groups[i].Types = append(groups[i].Types, gen.PackageDocType{Type: t, Count: n, Entries: entries})
			groups[i].Count += n
		}
	}
	out.Groups = &groups
	if warnings && len(tree.Warnings) > 0 {
		ws := make([]gen.PackageDocWarning, 0, len(tree.Warnings))
		for _, w := range tree.Warnings {
			ws = append(ws, gen.PackageDocWarning{Path: w.Path, Code: w.Code, Message: w.Message})
		}
		out.Warnings = &ws
	}
	return out
}

// typeOK and nameOK are the grammars of the path parameters of an entry: a type or a name outside them names no entry.
func typeOK(s string) bool { return grammar(s, false) }
func nameOK(s string) bool { return grammar(s, true) }

func grammar(s string, dots bool) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter, digit := c >= 'a' && c <= 'z', c >= '0' && c <= '9'
		if !letter && !digit && c != '_' && (!dots || c != '.') {
			return false
		}
	}
	return true
}

// GetPackageVersionDocEntry implements GET /api/v1/packages/{packageId}/versions/{version}/docs/entries/{type}/{name} (R-ANS-036).
func (s *Server) GetPackageVersionDocEntry(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath, typ, name string, params gen.GetPackageVersionDocEntryParams) {
	d, err := s.resolveDocs(r, id, version, params.Repository)
	if err == nil && (!typeOK(typ) || !nameOK(name) || d.state.State != rows.DocAvailable) {
		err = docsNotFound()
	}
	var e rows.DocEntry
	found := false
	if err == nil {
		e, found, err = d.st.DocEntry(d.ctx, d.repo.NumericID(), d.name, d.ver, typ, name)
	}
	if err == nil && !found {
		err = docsNotFound()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	writeTagged(w, r, entryOf(d, e))
}

// markupHTML converts the Ansible markup of a prose string and sanitizes the result with the README policy (R-ANS-039).
type markupHTML struct{ rd *readme.Renderer }

func (m markupHTML) html(s string) string { return m.rd.SanitizeHTML(docgen.RenderMarkup(s)) }

func (m markupHTML) list(v any) *[]string {
	var src []string
	switch t := v.(type) {
	case string:
		src = []string{t}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok {
				src = append(src, s)
			}
		}
	}
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	for i, s := range src {
		out[i] = m.html(s)
	}
	return &out
}

// entryOf builds the page of an entry from its row (R-ANS-036): the prose as sanitized HTML, everything else as text and typed values.
func entryOf(d *docsRead, e rows.DocEntry) gen.PackageDocEntry {
	m := markupHTML{rd: d.ps.Readme}
	out := gen.PackageDocEntry{
		Type: e.Type, Name: e.Name, FullName: d.name + "." + e.Name, HasDocs: e.HasDocs, Source: gen.PackageDocEntrySource(d.state.Source),
	}
	if e.ShortDescription != "" {
		sd := e.ShortDescription
		out.ShortDescription = &sd
	}
	if !d.state.BuiltAt.IsZero() {
		at := d.state.BuiltAt
		out.BuiltAt = &at
	}
	switch {
	case e.ReadmeMD != "":
		h := d.ps.Readme.Render(e.ReadmeMD).HTML
		out.ReadmeHtml = &h
	case e.ReadmeHTML != "":
		h := d.ps.Readme.SanitizeHTML(e.ReadmeHTML)
		out.ReadmeHtml = &h
	}
	if !e.HasDocs || len(e.Doc) == 0 {
		return out
	}
	var ds struct {
		Doc      map[string]any `json:"doc"`
		Return   []any          `json:"return"`
		Examples string         `json:"examples"`
	}
	if json.Unmarshal(e.Doc, &ds) != nil {
		return out
	}
	doc := ds.Doc
	out.VersionAdded = textOf(doc["version_added"])
	out.Description, out.Notes, out.Requirements = m.list(doc["description"]), m.list(doc["notes"]), m.list(doc["requirements"])
	out.Author = textList(doc["author"])
	out.ExtendsFragments = textList(doc["extends_documentation_fragment"])
	out.Options = optionsOf(m, doc["options"], 0)
	out.ReturnValues = returnsOf(m, ds.Return, 0)
	if ds.Examples != "" {
		ex := ds.Examples
		out.Examples = &ex
	}
	out.Seealso = seeAlsoOf(m, doc["seealso"])
	out.Attributes = attributesOf(m, doc["attributes"])
	out.Deprecated = deprecatedOf(m, doc["deprecated"])
	return out
}

func textOf(v any) *string {
	switch t := v.(type) {
	case string:
		return &t
	case float64:
		s := strconv.FormatFloat(t, 'f', -1, 64)
		return &s
	case bool:
		s := strconv.FormatBool(t)
		return &s
	}
	return nil
}

func textList(v any) *[]string {
	var out []string
	switch t := v.(type) {
	case string:
		out = []string{t}
	case []any:
		for _, x := range t {
			if s := textOf(x); s != nil {
				out = append(out, *s)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

func optionsOf(m markupHTML, v any, depth int) *[]gen.PackageDocOption {
	list, ok := v.([]any)
	if !ok || depth >= maxDocDepth {
		return nil
	}
	out := make([]gen.PackageDocOption, 0, len(list))
	for _, x := range list {
		o, ok := x.(map[string]any)
		name, _ := o["name"].(string)
		if !ok || name == "" {
			continue
		}
		opt := gen.PackageDocOption{
			Name: name, Type: textOf(o["type"]), Elements: textOf(o["elements"]), VersionAdded: textOf(o["version_added"]),
			Default: o["default"], Description: m.list(o["description"]), Aliases: textList(o["aliases"]),
		}
		if b, ok := o["required"].(bool); ok {
			opt.Required = &b
		}
		if c, ok := o["choices"].([]any); ok {
			opt.Choices = &c
		}
		opt.Suboptions = optionsOf(m, o["suboptions"], depth+1)
		out = append(out, opt)
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

func returnsOf(m markupHTML, list []any, depth int) *[]gen.PackageDocReturnValue {
	if depth >= maxDocDepth {
		return nil
	}
	out := make([]gen.PackageDocReturnValue, 0, len(list))
	for _, x := range list {
		o, ok := x.(map[string]any)
		name, _ := o["name"].(string)
		if !ok || name == "" {
			continue
		}
		rv := gen.PackageDocReturnValue{
			Name: name, Type: textOf(o["type"]), Elements: textOf(o["elements"]), Returned: textOf(o["returned"]),
			VersionAdded: textOf(o["version_added"]), Sample: o["sample"], Description: m.list(o["description"]),
		}
		if c, ok := o["contains"].([]any); ok {
			rv.Contains = returnsOf(m, c, depth+1)
		}
		out = append(out, rv)
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// seeAlsoOf keeps module, description and link of the entries of seealso; a link only for http and https.
func seeAlsoOf(m markupHTML, v any) *[]gen.PackageDocSeeAlso {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []gen.PackageDocSeeAlso
	for _, x := range list {
		o, ok := x.(map[string]any)
		if !ok {
			continue
		}
		var item gen.PackageDocSeeAlso
		for _, k := range []string{"module", "name", "plugin", "ref"} {
			if s := textOf(o[k]); s != nil && item.Module == nil {
				item.Module = s
			}
		}
		if s, ok := o["description"].(string); ok {
			h := m.html(s)
			item.Description = &h
		}
		if s, ok := o["link"].(string); ok {
			if l := strings.ToLower(s); strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
				item.Link = &s
			}
		}
		if item.Module != nil || item.Description != nil || item.Link != nil {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

func attributesOf(m markupHTML, v any) *[]gen.PackageDocAttribute {
	mp, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(mp))
	for k := range mp {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]gen.PackageDocAttribute, 0, len(names))
	for _, n := range names {
		spec, _ := mp[n].(map[string]any)
		out = append(out, gen.PackageDocAttribute{Name: n, Support: textOf(spec["support"]), Description: m.list(spec["description"]), Details: m.list(spec["details"])})
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

func deprecatedOf(m markupHTML, v any) *gen.PackageDocDeprecated {
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := gen.PackageDocDeprecated{RemovedIn: textOf(o["removed_in"]), RemovedAtDate: textOf(o["removed_at_date"])}
	if s, ok := o["why"].(string); ok {
		h := m.html(s)
		out.Why = &h
	}
	if s, ok := o["alternative"].(string); ok {
		h := m.html(s)
		out.Alternative = &h
	}
	return &out
}

// GetPackageVersionDocFile implements GET /api/v1/packages/{packageId}/versions/{version}/docs/files/{file} (R-ANS-036): the answer and
// its strong tag follow the README operation (R-API-029).
func (s *Server) GetPackageVersionDocFile(w http.ResponseWriter, r *http.Request, id gen.PackageId, version gen.PackageVersionPath, file string, params gen.GetPackageVersionDocFileParams) {
	d, err := s.resolveDocs(r, id, version, params.Repository)
	if err == nil && d.state.State != rows.DocAvailable {
		err = docsNotFound()
	}
	var f rows.DocFile
	found := false
	if err == nil {
		f, found, err = d.st.DocFile(d.ctx, d.repo.NumericID(), d.name, d.ver, file)
	}
	if err == nil && !found {
		err = docsNotFound()
	}
	if err != nil {
		httpserver.Problem(w, r, err)
		return
	}
	tag := docFileTag(d.ps.Readme, f)
	if NotModified(w, r, tag) {
		return
	}
	out := gen.PackageDocFile{Name: f.Name, ContentType: gen.PackageDocFileContentTypeTextmarkdown}
	if f.Format == rows.DocFormatHTML {
		out.Html = d.ps.Readme.SanitizeHTML(f.Body)
	} else {
		res := d.ps.Readme.Render(f.Body)
		out.Html, out.Truncated, out.ExternalImagesBlocked = res.HTML, res.Truncated, res.ExternalImagesBlocked
	}
	b, err := json.Marshal(out)
	if err != nil {
		httpserver.Problem(w, r, errs.Wrap(err, "api.writeJSON"))
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(b, '\n'))
}

// docFileTag is the strong tag of a file: the source, its format, the renderer version and the image policy (the README rule of R-API-029).
func docFileTag(rd *readme.Renderer, f rows.DocFile) string {
	h := sha256.New()
	h.Write([]byte(f.Format))
	h.Write([]byte{0})
	h.Write([]byte(f.Body))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(readme.Version)))
	h.Write([]byte{0})
	h.Write([]byte(rd.PolicyHash()))
	return `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// docsAnnouncement is the member metadata.docs of the version details of a package of the format Ansible Galaxy (R-ANS-036, Q-API-263). It
// is absent for a version that has none to announce and for a caller who may not read the wiki of the repositories that hold it.
func (s *Server) docsAnnouncement(r *http.Request, id gen.PackageId, version gen.PackageVersionPath) *gen.PackageVersionDocs {
	d, err := s.resolveDocs(r, id, version, nil)
	if err != nil {
		return nil
	}
	out := gen.PackageVersionDocs{State: gen.PackageVersionDocsState(d.state.State)}
	if d.state.State == rows.DocAvailable {
		src := gen.PackageVersionDocsSource(d.state.Source)
		n := int32(min(d.state.Entries, 1<<30)) //nolint:gosec // at most 5,000
		out.Source, out.Entries = &src, &n
	}
	return &out
}
