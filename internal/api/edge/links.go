package edge

import (
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/items"
)

// Links builds the absolute URLs a description carries from the public base URL
// (R-ARCH-007: content links come from one place). Base is the base URL the
// request's endpoint resolved (httpserver.InfoFrom(ctx).BaseURL).
type Links struct{ Base string }

func (l Links) base() string { return strings.TrimSuffix(l.Base, "/") }

// escapePath percent-encodes every segment of a canonical path.
func escapePath(p string) string {
	if p == "" {
		return ""
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// File is the URL of a file or folder below /files/.
func (l Links) File(repo, path string, folder bool) string {
	u := l.base() + "/files/" + url.PathEscape(repo) + "/" + escapePath(path)
	if folder && path != "" {
		u += "/"
	}
	return u
}

// Item is the URL of the item description in the management API.
func (l Links) Item(repo, path string) string {
	return l.base() + "/api/v1/repositories/" + url.PathEscape(repo) + "/items?path=" + url.QueryEscape(path)
}

// Children is the URL of the listing of a folder in the management API.
func (l Links) Children(repo, path string) string {
	return l.base() + "/api/v1/repositories/" + url.PathEscape(repo) + "/children?path=" + url.QueryEscape(path)
}

// Stamp is a time and the principal behind it (R-STOR-016 "created" and "modified").
type Stamp struct {
	At time.Time `json:"at"`
	By *string   `json:"by"`
}

// Digests is the digest set of a file, lower-case hexadecimal (R-STOR-002).
type Digests struct {
	SHA256 string `json:"sha256,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	MD5    string `json:"md5,omitempty"`
}

// ItemView is the description of an item that R-STOR-016 and the answer of a
// deploy (R-STOR-007) return. The package of an item is null: the generic format
// groups no items into packages (R-GENERIC-006). The upstream URL is that of an
// item a remote repository cached, null for an uploaded one.
type ItemView struct {
	Repository       string    `json:"repository"`
	Path             string    `json:"path"`
	Kind             string    `json:"kind"`
	Size             *int64    `json:"size,omitempty"`
	MediaType        *string   `json:"mediaType,omitempty"`
	Explicit         *bool     `json:"explicit,omitempty"`
	Created          Stamp     `json:"created"`
	Modified         Stamp     `json:"modified"`
	UpdatedAt        time.Time `json:"updatedAt"`
	Digests          *Digests  `json:"digests,omitempty"`
	DeclaredDigests  *Digests  `json:"declaredDigests,omitempty"`
	DeclaredMismatch *bool     `json:"declaredMismatch,omitempty"`
	ContentStatus    *string   `json:"contentStatus,omitempty"`
	Package          *struct{} `json:"package"`
	UpstreamURL      *string   `json:"upstreamUrl"`
	DownloadURL      *string   `json:"downloadUrl,omitempty"`
	ListingURL       *string   `json:"listingUrl,omitempty"`
	ItemURL          string    `json:"itemUrl"`
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DescribeItem renders the description of the item.
func DescribeItem(it items.Item, l Links) ItemView {
	v := ItemView{
		Repository: it.Repository, Path: it.Path, Kind: it.Kind,
		Created:   Stamp{At: it.CreatedAt, By: nonEmpty(it.CreatedBy)},
		Modified:  Stamp{At: it.ModifiedAt, By: nonEmpty(it.ModifiedBy)},
		UpdatedAt: it.UpdatedAt, ItemURL: l.Item(it.Repository, it.Path),
		UpstreamURL: nonEmpty(it.UpstreamURL),
	}
	if it.Kind == items.KindFolder {
		ex := it.Explicit
		listing := l.Children(it.Repository, it.Path)
		v.Explicit, v.ListingURL = &ex, &listing
		return v
	}
	size, mt := it.Size, it.MediaType
	if mt == "" {
		mt = "application/octet-stream"
	}
	v.Size, v.MediaType = &size, &mt
	v.Digests = &Digests{SHA256: it.Digest.Hex(), SHA512: hex.EncodeToString(it.SHA512), SHA1: hex.EncodeToString(it.SHA1), MD5: hex.EncodeToString(it.MD5)}
	if d := declared(it); d != nil {
		v.DeclaredDigests = d
	}
	mis, status := it.DeclaredMismatch, it.ContentStatus
	v.DeclaredMismatch, v.ContentStatus = &mis, &status
	dl := l.File(it.Repository, it.Path, false)
	v.DownloadURL = &dl
	return v
}

func declared(it items.Item) *Digests {
	d := Digests{}
	if len(it.DeclaredSHA256) > 0 {
		d.SHA256 = hex.EncodeToString(it.DeclaredSHA256)
	}
	if len(it.DeclaredSHA512) > 0 {
		d.SHA512 = hex.EncodeToString(it.DeclaredSHA512)
	}
	if len(it.DeclaredSHA1) > 0 {
		d.SHA1 = hex.EncodeToString(it.DeclaredSHA1)
	}
	if len(it.DeclaredMD5) > 0 {
		d.MD5 = hex.EncodeToString(it.DeclaredMD5)
	}
	if d == (Digests{}) {
		return nil
	}
	return &d
}
