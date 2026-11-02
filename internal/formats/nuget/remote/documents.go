package remote

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/guided-traffic/trove/internal/errs"
)

// Decode returns the JSON of a stored upstream document. A registration hive may be stored
// gzip-coded whatever the request asked for - the upstream's blob carries the coding itself -
// so a stream that starts with the gzip magic is inflated, bounded: the result is never larger
// than max bytes (R-ARCH-028). Anything else is returned as it is, and the bound applies to it
// too.
func Decode(doc []byte, max int64) ([]byte, error) {
	if len(doc) >= 2 && doc[0] == 0x1f && doc[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(doc))
		if err != nil {
			return nil, upstream("The upstream document is not a valid gzip stream: " + shorten(err.Error()))
		}
		defer func() { _ = zr.Close() }()
		out, err := io.ReadAll(io.LimitReader(zr, max+1))
		if err != nil {
			return nil, upstream("The upstream document is not a valid gzip stream: " + shorten(err.Error()))
		}
		doc = out
	}
	if int64(len(doc)) > max {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge, "The upstream document is larger than the limit of documents held in memory.")
	}
	return doc, nil
}

// VersionList reads the version list of the flat container, {"versions": [...]}, and returns
// the version strings as the upstream wrote them. Normalizing them is the caller's (the
// version rules are R-NUGET-003's).
func VersionList(doc []byte, max int64) ([]string, error) {
	doc, err := Decode(doc, max)
	if err != nil {
		return nil, err
	}
	var d struct {
		Versions *[]string `json:"versions"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, invalid("The upstream's version list is not the document {\"versions\": [...]}: " + shorten(err.Error()))
	}
	if d.Versions == nil {
		return nil, invalid("The upstream's version list has no \"versions\" member.")
	}
	return *d.Versions, nil
}

// invalid is the error of a document that is not what its path promises.
func invalid(detail string) error {
	return errs.E(errs.UpstreamFailed, errs.CodeNuGetInvalidUpstreamDocument, detail)
}

// Leaf is one version of a registration page as the upstream wrote it.
type Leaf struct {
	// PackageContent is the address of the package the leaf names: its own member, else the
	// catalog entry's. Empty when the upstream gave none.
	PackageContent string
	// Entry is the catalog entry, member by member.
	Entry map[string]json.RawMessage
}

// PageRef is a page of a registration index: its bounds as the upstream wrote them, its
// address, and its leaves when the index inlines them.
type PageRef struct {
	// ID is the address of the page; for an inlined page it is the index address with a
	// fragment.
	ID           string
	Lower, Upper string
	// Inline is true when the index holds the leaves, in Leaves.
	Inline bool
	Leaves []Leaf
}

// Index is a registration index: the pages in the order the upstream listed them.
type Index struct{ Pages []PageRef }

type rawLeaf struct {
	PackageContent string                     `json:"packageContent"`
	CatalogEntry   map[string]json.RawMessage `json:"catalogEntry"`
}

type rawPage struct {
	ID    string          `json:"@id"`
	Lower string          `json:"lower"`
	Upper string          `json:"upper"`
	Items json.RawMessage `json:"items"`
}

// leaves reads the leaves of a page, at most limit of them: a page that holds more is refused
// before a leaf is decoded, so a fill never converts or maps more leaves than one rendering reads
// (Q-NUGET-83).
func (p rawPage) leaves(limit int) ([]Leaf, bool, error) {
	if len(bytes.TrimSpace(p.Items)) == 0 || string(bytes.TrimSpace(p.Items)) == "null" {
		return nil, false, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(p.Items, &items); err != nil {
		return nil, true, invalid("A page of the upstream's registration has leaves that are no list of leaf objects: " + shorten(err.Error()))
	}
	if len(items) > limit {
		return nil, true, invalid("The upstream's registration lists more than " + strconv.Itoa(MaxLeaves) + " versions; trove reads at most that many of one ID.")
	}
	raw := make([]rawLeaf, len(items))
	for i, it := range items {
		if err := json.Unmarshal(it, &raw[i]); err != nil {
			return nil, true, invalid("A page of the upstream's registration has leaves that are no list of leaf objects: " + shorten(err.Error()))
		}
	}
	out := make([]Leaf, 0, len(raw))
	for _, l := range raw {
		pc := l.PackageContent
		if pc == "" {
			pc = stringOf(l.CatalogEntry["packageContent"])
		}
		out = append(out, Leaf{PackageContent: pc, Entry: l.CatalogEntry})
	}
	return out, true, nil
}

// MaxPages is the most pages one registration index may list, inlined or not (Q-NUGET-83). Every
// page that is not inlined costs an upstream request, a stored document and possibly a URL-map
// row when the index is read, so an index that lists more is no document trove reads: 512 pages of
// nuget.org's 64 leaves are 32,768 versions, which is MaxLeaves.
const MaxPages = 512

// MaxLeaves is the most versions one rendering of a registration reads, over the index and its
// pages together (Q-NUGET-83): the leaves are converted to rows held in memory, and a page of tiny
// leaves would otherwise turn the byte bound into millions of them.
const MaxLeaves = 32768

// ParseIndex reads a registration index (R-NUGET-009 shape): a list of pages, each inlined
// with its leaves or listed by bounds and address. The coding of the stored bytes is undone
// first (Decode). An index that lists more than MaxPages pages, or inlines more than MaxLeaves
// leaves over all its pages, is refused.
func ParseIndex(doc []byte, max int64) (*Index, error) {
	doc, err := Decode(doc, max)
	if err != nil {
		return nil, err
	}
	return parseIndex(doc)
}

func parseIndex(doc []byte) (*Index, error) {
	var d struct {
		Items []rawPage `json:"items"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, invalid("The upstream's registration index is not a registration document: " + shorten(err.Error()))
	}
	if len(d.Items) > MaxPages {
		return nil, invalid("The upstream's registration index lists " + strconv.Itoa(len(d.Items)) + " pages; trove reads at most " + strconv.Itoa(MaxPages) + ".")
	}
	idx := &Index{Pages: make([]PageRef, 0, len(d.Items))}
	left := MaxLeaves
	for _, p := range d.Items {
		leaves, inline, err := p.leaves(left)
		if err != nil {
			return nil, err
		}
		if !inline && (p.ID == "" || p.Lower == "" || p.Upper == "") {
			return nil, invalid("A page of the upstream's registration index names neither its address nor its bounds.")
		}
		left -= len(leaves)
		idx.Pages = append(idx.Pages, PageRef{ID: p.ID, Lower: p.Lower, Upper: p.Upper, Inline: inline, Leaves: leaves})
	}
	return idx, nil
}

// ParsePage reads a registration page fetched on its own; a page of more than MaxLeaves leaves is
// refused.
func ParsePage(doc []byte, max int64) ([]Leaf, error) {
	doc, err := Decode(doc, max)
	if err != nil {
		return nil, err
	}
	return parsePage(doc)
}

func parsePage(doc []byte) ([]Leaf, error) {
	var p rawPage
	if err := json.Unmarshal(doc, &p); err != nil {
		return nil, invalid("The upstream's registration page is not a registration document: " + shorten(err.Error()))
	}
	leaves, inline, err := p.leaves(MaxLeaves)
	if err != nil {
		return nil, err
	}
	if !inline {
		return nil, invalid("The upstream's registration page holds no leaves.")
	}
	return leaves, nil
}

// stringOf reads a JSON string, "" for anything else.
func stringOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// ContentURL validates an address an upstream document names for a package or a page: absolute
// http or https, with a host, no user info. The fragment goes. The host is the engine's to
// judge (R-REPO-043).
func ContentURL(raw string) (*url.URL, error) {
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
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}
