package oci

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/oci/rows"
)

// The answer of the referrers API is an OCI image index (R-OCI-016).
const (
	mediaTypeImageIndex = "application/vnd.oci.image.index.v1+json"
	// MaxReferrersBytes bounds the descriptors of one page; a longer list continues with a Link (R-OCI-016).
	MaxReferrersBytes = 4 << 20
	// maxArtifactTypeBytes bounds the artifactType filter, as the column is (R-OCI-007).
	maxArtifactTypeBytes = 255
)

// referrerDescriptor is one descriptor of the answer: the manifest, with its artifact type and its annotations.
type referrerDescriptor struct {
	MediaType    string          `json:"mediaType"`
	Digest       string          `json:"digest"`
	Size         int64           `json:"size"`
	ArtifactType string          `json:"artifactType,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

// referrersIndex is the answer.
type referrersIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Manifests     []referrerDescriptor `json:"manifests"`
}

// referrersLink is the Link header of a page that is followed by more: the same path with the filter and the digest the page ended at.
func referrersLink(rq *formats.Request, artifactType, last string) string {
	v := url.Values{}
	if artifactType != "" {
		v.Set("artifactType", artifactType)
	}
	v.Set("last", last)
	return "<" + rq.URL.EscapedPath() + "?" + v.Encode() + `>; rel="next"`
}

// handleReferrers is GET /v2/<p>/<r>/<name>/referrers/<digest> (R-OCI-016): an image index with one descriptor per manifest of the
// name whose subject is the digest, in digest order, read from the rows of R-OCI-007 so that concurrent pushes of referrers never
// lose one. No referrer, or a subject the repository does not hold, is an empty index with 200. ?artifactType= filters and is
// answered with OCI-Filters-Applied. A page holds at most 1,000 descriptors and 4 MiB; a longer list continues with a Link. The
// referrers tag schema is not maintained (R-OCI-016): the API exists from the start, so a client never falls back to it.
func (f *Format) handleReferrers(ctx context.Context, rq *formats.Request, w http.ResponseWriter) error {
	if rq.Repo.Class != formats.Local {
		return notImplemented("list referrers of a remote or a virtual repository (R-OCI-024, R-OCI-027)")
	}
	name := rq.Params.Get("name")
	subject, ok := digestFromText(rq.Params.Get("digest"))
	if !ok {
		return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The digest is not a sha256 digest.")
	}
	// The gate is the subject's manifest path, the item the request asks about (R-AUTHZ-005, Q-OCI-140); each referrer is decided by its own path.
	if err := rq.Host.Authorize(ctx, rq, formats.Target{Package: name, Path: ManifestPath(name, digestText(subject)), Actions: []formats.Action{formats.ActionRead}}); err != nil {
		return err
	}
	query := rq.URL.Query()
	artifactType := query.Get("artifactType")
	filtered := artifactType != ""
	if len(artifactType) > maxArtifactTypeBytes {
		return errs.E(errs.Invalid, errs.CodeOCIUnsupported, "The parameter artifactType is longer than 255 bytes.")
	}
	var after *[32]byte
	if last := query.Get("last"); last != "" {
		d, ok := digestFromText(last)
		if !ok {
			return errs.E(errs.Invalid, errs.CodeOCIDigestInvalid, "The parameter last is not a sha256 digest.")
		}
		after = &d
	}
	st, err := portOf(rq.Host.FormatData(ID))
	if err != nil {
		return err
	}
	pg, err := readReferrers(ctx, st, rq.RepoRef().ID, name, subject, artifactType, after, func(ms []rows.Manifest) ([]bool, error) {
		paths := make([]string, len(ms))
		for i, m := range ms {
			paths[i] = ManifestPath(name, digestText(m.Digest))
		}
		return rq.Host.PathsAllowed(ctx, rq, formats.ActionRead, paths)
	})
	if err != nil {
		return err
	}
	doc := referrersIndex{SchemaVersion: 2, MediaType: mediaTypeImageIndex, Manifests: pg.descs}
	h := w.Header()
	if filtered {
		h.Set("OCI-Filters-Applied", "artifactType")
	}
	if pg.cut {
		h.Set("Trove-Annotations-Cut", "true")
	}
	if pg.more {
		h.Set("Link", referrersLink(rq, artifactType, digestText(pg.cursor)))
	}
	h.Set("Cache-Control", "no-cache")
	return jsonReply(w, http.StatusOK, mediaTypeImageIndex, doc)
}

// referrersChunk is the number of rows read per statement: 4 MiB of page over the 64 KiB a row's annotations may take (R-OCI-007).
const referrersChunk = 64

// referrersScanBudget bounds the rows one request reads past without listing them (the ones the caller may not see): a page that has not
// filled by then ends with a Link at the last row read.
const referrersScanBudget = 4 * MaxPage

// referrersPage is one page of the answer: its descriptors, whether the list goes on after it, whether an annotation set in it was cut,
// and the digest the next page starts after.
type referrersPage struct {
	descs  []referrerDescriptor
	more   bool
	cut    bool
	cursor [32]byte
}

// descriptorOf is the descriptor of a row as the answer encodes it, with its encoding: the size of a page is measured on the bytes of the
// answer, not on the form PostgreSQL returns a jsonb in (which has a space after every colon and comma).
func descriptorOf(m rows.Manifest) (referrerDescriptor, int, bool) {
	d := referrerDescriptor{MediaType: m.MediaType, Digest: digestText(m.Digest), Size: m.Size, ArtifactType: m.ArtifactType}
	if len(m.Annotations) > 0 && string(m.Annotations) != "null" {
		d.Annotations = m.Annotations
	}
	enc, err := json.Marshal(d)
	return d, len(enc), err == nil
}

// readReferrers reads the rows of a page in chunks and keeps the ones allow admits, until the page is full: MaxPage descriptors or
// MaxReferrersBytes of the answer's own encoding. The row that does not fit is not listed and ends the page with a Link, so a list that
// goes on is never answered without one. A single statement for 1,001 rows would read up to 64 MiB of annotations for a page of 4 MiB,
// at the cost of every reader of a name somebody pushed fat referrers to.
func readReferrers(ctx context.Context, st Store, repoID int64, name string, subject [32]byte, artifactType string, after *[32]byte, allow func([]rows.Manifest) ([]bool, error)) (referrersPage, error) {
	pg := referrersPage{descs: []referrerDescriptor{}}
	total, scanned := 0, 0
	for {
		chunk, err := st.Referrers(ctx, repoID, name, subject, artifactType, after, referrersChunk)
		if err != nil {
			return pg, err
		}
		allowed, err := allow(chunk)
		if err != nil {
			return pg, err
		}
		for i, m := range chunk {
			scanned++
			if !allowed[i] {
				continue
			}
			d, n, ok := descriptorOf(m)
			if !ok {
				continue
			}
			if len(pg.descs) >= MaxPage || (len(pg.descs) > 0 && total+n+1 > MaxReferrersBytes) {
				pg.more = true
				return pg, nil
			}
			total += n + 1
			pg.descs = append(pg.descs, d)
			pg.cut = pg.cut || m.AnnotationsCut
			// The cursor is the last row listed: a row the caller may not read is never named in a Link.
			pg.cursor = m.Digest
		}
		if len(chunk) < referrersChunk {
			return pg, nil
		}
		if scanned >= referrersScanBudget {
			if len(pg.descs) == 0 {
				return pg, errs.E(errs.Invalid, errs.CodeOCIUnsupported,
					"More than 4,000 consecutive referrers of the digest are not readable for the caller; the list cannot go on without naming one of them.")
			}
			pg.more = true
			return pg, nil
		}
		last := chunk[len(chunk)-1].Digest
		after = &last
	}
}
