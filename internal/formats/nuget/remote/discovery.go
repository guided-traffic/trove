// Package remote holds what the NuGet format reads of an upstream feed (R-NUGET-014,
// R-NUGET-015): the service index and the addresses it announces, the version list, the
// registration index and its pages, and the gzip coding some upstreams store their hives in.
// It reads bytes and writes values; it holds no host, no request and no clock, so the same
// stored document always gives the same result on every replica (R-NUGET-030).
//
// Every document here is upstream input: sizes are bounded before they are read into memory
// (R-ARCH-028), and an address the upstream names is checked for its scheme and shape here
// and for its host by the engine (R-REPO-043).
package remote

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// The resource types R-NUGET-014 picks from the service index, in order of preference. They are
// functions so that no package-level slice exists (R-ARCH-032).
func typesContent() []string { return []string{"PackageBaseAddress/3.0.0"} }

func typesRegistration() []string {
	return []string{"RegistrationsBaseUrl/3.6.0", "RegistrationsBaseUrl/3.4.0", "RegistrationsBaseUrl"}
}
func typesSearch() []string { return []string{"SearchQueryService/3.5.0", "SearchQueryService"} }
func typesAutocomplete() []string {
	return []string{"SearchAutocompleteService/3.5.0", "SearchAutocompleteService"}
}

// Discovery is what a repository's upstream announces in its service index: where the
// resources are. Resources may live on other hosts than the index (nuget.org serves search and
// symbols from other hosts of its domain); whether a host may be contacted is decided by the
// remote engine from the upstream origin and remote.extraHosts, not here.
type Discovery struct {
	// Content is the base of the package content (flat container), PackageBaseAddress/3.0.0.
	Content *url.URL
	// Registration is the base of the registration hive picked: RegistrationsBaseUrl/3.6.0,
	// else /3.4.0, else the unversioned type.
	Registration *url.URL
	// RegistrationType is the @type that was picked.
	RegistrationType string
	// Search and Autocomplete are nil when the upstream announces none.
	Search, Autocomplete *url.URL
}

type indexDoc struct {
	Resources []struct {
		ID   string          `json:"@id"`
		Type json.RawMessage `json:"@type"`
	} `json:"resources"`
}

// ParseDiscovery reads a service index (R-NUGET-014): the package content resource and at
// least one registration resource are required, the rest is optional. A document that is no
// service index, or that lacks a required resource, is an upstream failure naming what is
// missing (Q-NUGET-14: the first fetch of a misconfigured remote answers 502).
func ParseDiscovery(doc []byte) (*Discovery, error) {
	var idx indexDoc
	if err := json.NewDecoder(bytes.NewReader(doc)).Decode(&idx); err != nil {
		return nil, upstream("The upstream's service index is not a JSON document: " + shorten(err.Error()))
	}
	if len(idx.Resources) == 0 {
		return nil, upstream("The upstream's service index lists no resources, so it is no NuGet v3 feed.")
	}
	byType := map[string]string{}
	for _, r := range idx.Resources {
		for _, t := range typeNames(r.Type) {
			k := strings.ToLower(t)
			if _, dup := byType[k]; !dup { // the first resource of a type wins, as in the client
				byType[k] = r.ID
			}
		}
	}
	pick := func(types []string) (string, *url.URL, error) {
		for _, t := range types {
			raw, ok := byType[strings.ToLower(t)]
			if !ok {
				continue
			}
			u, err := base(raw)
			if err != nil {
				return "", nil, upstream("The service index names the resource " + t + " with an address that cannot be used: " + err.Error())
			}
			return t, u, nil
		}
		return "", nil, nil
	}
	d := &Discovery{}
	var err error
	if _, d.Content, err = pick(typesContent()); err != nil {
		return nil, err
	}
	if d.Content == nil {
		return nil, upstream("The upstream's service index lacks the resource PackageBaseAddress/3.0.0; trove cannot restore packages from it. A NuGet v2 feed is not supported.")
	}
	if d.RegistrationType, d.Registration, err = pick(typesRegistration()); err != nil {
		return nil, err
	}
	if d.Registration == nil {
		return nil, upstream("The upstream's service index lacks a RegistrationsBaseUrl resource, so package metadata cannot be read.")
	}
	if _, d.Search, err = pick(typesSearch()); err != nil {
		return nil, err
	}
	if _, d.Autocomplete, err = pick(typesAutocomplete()); err != nil {
		return nil, err
	}
	return d, nil
}

// typeNames reads an @type, a string or a list of strings.
func typeNames(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

// base parses the address of a resource: absolute http or https, a host, no user info, and a
// path that ends in a slash so that the resource's documents are joined below it. A query and
// a fragment are dropped.
func base(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("it does not parse")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("it is no absolute http or https address")
	}
	if u.User != nil {
		return nil, errors.New("it carries user info")
	}
	u.Fragment, u.RawFragment, u.RawQuery, u.ForceQuery = "", "", "", false
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	return u, nil
}

// Join returns base with the segments appended, each as one path segment: a segment that is
// empty, a dot, a dot-dot or holds a NUL or a backslash never reaches the wire (R-ARCH-005).
func Join(base *url.URL, segments ...string) (*url.URL, error) {
	u := *base
	u.User, u.Fragment, u.RawFragment, u.RawQuery = nil, "", "", ""
	path := strings.TrimSuffix(base.Path, "/")
	raw := strings.TrimSuffix(base.EscapedPath(), "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "\x00\\") {
			return nil, errs.E(errs.Invalid, errs.CodeInvalidPath, "The path has a segment that cannot be sent to an upstream.")
		}
		path += "/" + s
		raw += "/" + url.PathEscape(s)
	}
	u.Path, u.RawPath = path, raw
	return &u, nil
}

// SearchURL returns the address of the search resource for a query: the parameters sorted by
// name, so that equal queries share one cached answer. ok is false when the upstream announced
// no search resource.
func (d *Discovery) SearchURL(params url.Values) (u *url.URL, ok bool) {
	if d == nil || d.Search == nil {
		return nil, false
	}
	c := *d.Search
	c.Path, c.RawPath = strings.TrimSuffix(c.Path, "/"), strings.TrimSuffix(c.RawPath, "/")
	c.RawQuery = params.Encode() // sorted by key
	return &c, true
}

// upstream is the error of an upstream document trove cannot use.
func upstream(detail string) error {
	return errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, detail)
}

// shorten bounds the text of a parser's message that goes into a detail.
func shorten(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
