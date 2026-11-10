package ansible

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
)

// Roles through a virtual repository (R-ANS-022, Q-ANS-19). A role exists only in a remote member whose upstream has the v1 API (R-ANS-020),
// and the upstream's role ids are numbers of that upstream, so two members can hold different roles under one id. The id a virtual
// repository serves therefore names its member: "<member key>-<upstream id>". The versions and archive routes decode it to the member
// and the upstream's id and ask that member, through the handler of the remote class on a request derived for the member. Local members
// take no part: a local repository has no roles.
//
// The documents are the member's own, rewritten on the way out: every role route in them ("v1/roles/<id>/…", "v1/roles/archives/<id>/…",
// whatever address precedes it) takes the id of the member, so a link, a download address and a page link of the member's answer lead
// back through the virtual repository.

// roleIDRE is a role route of a document, with the upstream's numeric id.
var roleIDRE = regexp.MustCompile(`(v1/roles/(?:archives/)?)([0-9]{1,12})/`)

// maxRoleAnswer bounds a role document held in memory for the rewrite (R-ANS-031).
const maxRoleAnswer = MaxDocumentBytes

// encodeRoleID is the id a virtual repository serves for the role id of a member's upstream.
func encodeRoleID(member, id string) string { return member + "-" + id }

// decodeRoleID reads the id of a served role: the part after the last "-" is the upstream's id (digits), the rest the member key.
func decodeRoleID(s string) (member, id string, ok bool) {
	i := strings.LastIndexByte(s, '-')
	if i <= 0 || !digits(s[i+1:], maxRoleIDDigits) {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// rewriteRoleRoutes gives every role route in a document the id of the member.
func rewriteRoleRoutes(doc []byte, member string) []byte {
	return roleIDRE.ReplaceAllFunc(doc, func(m []byte) []byte {
		sub := roleIDRE.FindSubmatch(m)
		return []byte(string(sub[1]) + encodeRoleID(member, string(sub[2])) + "/")
	})
}

// capture collects the answer of a member's handler in memory.
type capture struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (c *capture) Header() http.Header {
	if c.h == nil {
		c.h = http.Header{}
	}
	return c.h
}

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	if c.buf.Len()+len(b) > maxRoleAnswer {
		return 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A role document is larger than the limit of documents held in memory.")
	}
	return c.buf.Write(b)
}

// memberHandler finds the handler of the remote class for a role route.
func (f *Format) memberHandler(route string) (formats.HandlerFunc, error) {
	handlerRegistry.mu.RLock()
	build := handlerRegistry.by[formats.Remote][route]
	handlerRegistry.mu.RUnlock()
	if build == nil {
		return nil, notImplemented(route, formats.Remote)
	}
	return build(f), nil
}

// withParams is the request for a member with the parameters of the route set to the member's own.
func withParams(m *formats.Request, params formats.Params) *formats.Request {
	c := *m
	c.Params = params
	return &c
}

func errNoSuchRole() error {
	return errs.E(errs.NotFound, errs.CodeNotFound, "No remote member of this repository holds the role.")
}

// virtualRolesFind answers the lookup of a role by the first remote member whose answer is not empty (Q-ANS-19) and gives the roles of
// the answer the id of that member.
func (f *Format) virtualRolesFind(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	members, err := f.remoteMembers(ctx, rq, "roles/lookup.json")
	if err != nil {
		return err
	}
	handle, err := f.memberHandler(routeRolesFind)
	if err != nil {
		return err
	}
	var (
		empty   *capture
		failure error
	)
	for _, m := range members {
		c := &capture{}
		if err := handle(ctx, m.rq, c); err != nil {
			if failure == nil && errs.KindOf(err) != errs.NotFound && errs.KindOf(err) != errs.Forbidden {
				failure = err
			}
			continue
		}
		key := m.planned.Member.Name.Key
		body, count, err := rewriteRoleFind(c.buf.Bytes(), key)
		if err != nil {
			if failure == nil {
				failure = err
			}
			continue
		}
		c.buf.Reset()
		c.buf.Write(body)
		if count > 0 {
			return writeCaptured(w, c)
		}
		if empty == nil {
			empty = c
		}
	}
	switch {
	case empty != nil:
		return writeCaptured(w, empty)
	case failure != nil:
		return failure
	}
	return errNoSuchRole()
}

// rewriteRoleFind gives the roles of a lookup answer the id of the member and returns the number of roles.
func rewriteRoleFind(body []byte, member string) ([]byte, int, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's role lookup is not a document the registry reads.", errs.WithCause(err))
	}
	var results []map[string]json.RawMessage
	if raw, ok := doc["results"]; ok {
		if err := json.Unmarshal(raw, &results); err != nil {
			return nil, 0, errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "A member's role lookup is not a document the registry reads.", errs.WithCause(err))
		}
	}
	for _, r := range results {
		var id json.Number
		if json.Unmarshal(r["id"], &id) != nil || !digits(id.String(), maxRoleIDDigits) {
			continue
		}
		enc, _ := json.Marshal(encodeRoleID(member, id.String()))
		r["id"] = enc
	}
	if results != nil {
		enc, err := json.Marshal(results)
		if err != nil {
			return nil, 0, wrapInternal(err)
		}
		doc["results"] = enc
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, wrapInternal(err)
	}
	return rewriteRoleRoutes(out, member), len(results), nil
}

// writeCaptured writes the answer of a member as the answer of the virtual repository.
func writeCaptured(w http.ResponseWriter, c *capture) error {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if cs := c.Header().Get("Cache-Status"); cs != "" {
		h.Set("Cache-Status", cs)
	}
	h.Set("Content-Length", strconv.Itoa(c.buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(c.buf.Bytes())
	return err
}

// roleMember finds the remote member a served role id names among the members the caller may read, and the request for it with the
// parameters of the route set to the upstream's own. An id that names a member not in the repository, or one without a v1 API, is 404.
func (f *Format) roleMember(ctx context.Context, rq *formats.Request, path string, extra formats.Params) (*formats.Request, string, error) {
	member, id, ok := decodeRoleID(rq.Params.Get(paramID))
	if !ok {
		return nil, "", errNoSuchRole()
	}
	members, err := f.remoteMembers(ctx, rq, path)
	if err != nil {
		return nil, "", err
	}
	for _, m := range members {
		if m.planned.Member.Name.Key != member {
			continue
		}
		params := formats.Params{paramID: id}
		for k, v := range extra {
			params[k] = v
		}
		return withParams(m.rq, params), member, nil
	}
	return nil, "", errNoSuchRole()
}

// virtualRoleVersions answers the versions of a role from the member its id names.
func (f *Format) virtualRoleVersions(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	mrq, member, err := f.roleMember(ctx, rq, "roles/lookup.json", nil)
	if err != nil {
		return err
	}
	handle, err := f.memberHandler(routeRoleVersions)
	if err != nil {
		return err
	}
	c := &capture{}
	if err := handle(ctx, mrq, c); err != nil {
		return err
	}
	body := rewriteRoleRoutes(c.buf.Bytes(), member)
	c.buf.Reset()
	c.buf.Write(body)
	return writeCaptured(w, c)
}

// virtualRoleArchive serves the archive of a role from the member its id names; the member's handler streams it with its own headers.
func (f *Format) virtualRoleArchive(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	mrq, _, err := f.roleMember(ctx, rq, "roles/lookup.json", formats.Params{paramFile: rq.Params.Get(paramFile)})
	if err != nil {
		return err
	}
	handle, err := f.memberHandler(routeRoleArchive)
	if err != nil {
		return err
	}
	return handle(ctx, mrq, w)
}
