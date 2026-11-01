package nuget

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/document"
)

// RenderVersion is the version of the rendering of the documents of this format. It is part
// of every ETag, so a release that must make clients drop the documents they cached raises
// it (R-NUGET-002, Q-NUGET-9).
const RenderVersion = 1

// serviceIndexVersion is the "version" member of the service index (R-NUGET-002).
const serviceIndexVersion = "3.0.0"

// cacheSuffix is the suffix of the key of the cache view of a remote repository
// (R-REPO-032).
const cacheSuffix = "~cache"

// resource is one entry of the service index.
type resource struct {
	ID   string `json:"@id"`
	Type string `json:"@type"`
}

// serviceIndex is the service index document (R-NUGET-002).
type serviceIndex struct {
	Version   string     `json:"version"`
	Resources []resource `json:"resources"`
}

// ServiceIndex renders the service index of the repository whose v3 root is feed (with its
// trailing slash, "<base>/api/nuget/<key>/v3/"). Because a client only looks up the @type
// values it knows, every resource is listed under every alias of the table of R-NUGET-002.
// The publish resources are listed when the repository takes pushes: a local repository,
// and a virtual one with a deployment target (R-NUGET-019). The resources of the P1
// requirements (autocomplete, vulnerability information, the README template) are listed
// when they are served.
func ServiceIndex(feed string, publish bool) []byte {
	var rs []resource
	add := func(path string, types ...string) {
		for _, t := range types {
			rs = append(rs, resource{ID: feed + path, Type: t})
		}
	}
	add(resFlatContainer+"/", "PackageBaseAddress/3.0.0")
	if publish {
		add(resPackage, "PackagePublish/2.0.0")
		add(resSymbolPackage, "SymbolPackagePublish/4.9.0")
	}
	add(resRegistration+"/", "RegistrationsBaseUrl", "RegistrationsBaseUrl/3.0.0-beta", "RegistrationsBaseUrl/3.0.0-rc")
	add(resRegistrationGz+"/", "RegistrationsBaseUrl/3.4.0")
	add(resRegistrationSV2+"/", "RegistrationsBaseUrl/3.6.0")
	add(resSearch, "SearchQueryService", "SearchQueryService/3.0.0-beta", "SearchQueryService/3.0.0-rc", "SearchQueryService/3.5.0")
	b, err := json.Marshal(serviceIndex{Version: serviceIndexVersion, Resources: rs})
	if err != nil { // cannot happen: strings only
		panic(err)
	}
	return b
}

// takesPushes reports whether the service index lists the publish resources for the
// repository the request addressed.
func takesPushes(rq *formats.Request) bool { return rq.Repo.Class == formats.Local }

// indexETag is the weak validator of the service index (R-NUGET-002): a hash of the inputs
// of the rendering, computed without rendering. The inputs are the render version, the
// repository (its id, and the key it is served under, which the URLs of the document
// carry) and its configuration version, the base URL, whether the publish resources
// are listed, and the endpoint of the request with its exposure fingerprint (R-NUGET-034).
// Every replica sends the same value for the same inputs.
func indexETag(rq *formats.Request, base string, publish bool) string {
	h := sha256.New()
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field("nuget-service-index")
	field(strconv.Itoa(RenderVersion))
	field(rq.Repo.ID)
	field(rq.Addressed() + servingSuffix(rq))
	field(strconv.FormatInt(rq.Repo.Version, 10))
	field(base)
	field(strconv.FormatBool(publish))
	field(rq.Endpoint.Name)
	field(rq.Endpoint.ExposureFingerprint)
	return `W/"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// ifNoneMatch reports whether an If-None-Match header holds the ETag, by the weak
// comparison of RFC 9110 section 8.8.3.2. The wildcard never matches: a 200 costs bytes and
// never correctness.
func ifNoneMatch(header, etag string) bool { return document.IfNoneMatch(header, etag) }

// jsonContentType is the Content-Type of every JSON document of the protocol (R-NUGET-002).
const jsonContentType = document.ContentType

// handleIndex serves the service index at its three spellings with one body (R-NUGET-001,
// R-NUGET-002). It is static per repository: nothing is read from storage, so the permission
// is the repository-level read of the coarse check, and an unauthenticated client is
// challenged here first (R-NUGET-012).
func (f *Format) handleIndex(_ context.Context, rq *formats.Request, w http.ResponseWriter) error {
	links := rq.Host.Links(rq)
	feed := links.Registry(rq.Addressed()+servingSuffix(rq)) + FeedSegment + "/"
	publish := takesPushes(rq)
	etag := indexETag(rq, links.Base(), publish)
	h := w.Header()
	h.Set("ETag", etag)
	if ifNoneMatch(rq.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	body := ServiceIndex(feed, publish)
	h.Set("Content-Type", jsonContentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if rq.Method == http.MethodHead {
		return nil
	}
	_, err := w.Write(body)
	return err
}

// servingSuffix is the suffix of the key a document is served under: the cache view of a
// remote repository serves under "<key>~cache" (R-REPO-032), so the URLs it hands out stay
// in the view.
func servingSuffix(rq *formats.Request) string {
	if rq.CacheView {
		return cacheSuffix
	}
	return ""
}
