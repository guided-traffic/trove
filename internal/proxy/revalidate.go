package proxy

import (
	"context"
	"net/http"
)

// Revalidator is what the engine needs of a format that revalidates an expired stored document by a request of its own kind
// instead of the conditional GET (R-OCI-022): a container registry's HEAD, whose digest header says whether a tag still points
// at the stored manifest, costs no manifest pull of the upstream's quota. internal/formathost adapts a formats.Revalidator to it.
type Revalidator interface {
	// RevalidationRequest returns the request for the expired stored document; ok false takes the engine's conditional GET. get is
	// the GET the engine would send, without validators and with the URL map applied: the request derives from it, so that its
	// answer describes the representation the GET would fetch. Like every upstream request it carries no Authorization header: the
	// client adds the credential where it may go, and a challenge runs the format's handshake.
	RevalidationRequest(ctx context.Context, b UpstreamBase, path, variant string, stored Document, get *http.Request) (req *http.Request, ok bool, err error)
	// Unchanged reads the 2xx answer: true renews the stored document's freshness without a body; false makes the engine fetch
	// the document with an unconditional GET. The engine reads every other status itself and never asks: a 404 or 410 is the upstream's "gone"
	// (R-REPO-029), a 429 or a 5xx follows the stale rules and the circuit.
	Unchanged(path string, stored Document, resp *http.Response) bool
}

// revalidate is the revalidation of the stored document this fetch was started for, when the adapter has its own request for it.
// done is true when the revalidation is the fetch's result: the document was renewed without a body, or the upstream's answer
// ended the fetch (gone, a failure, a rate limit). done is false when the document is to be fetched whole.
func (f *fetcher) revalidate(ctx context.Context) (res flightResult, done bool) {
	rq := f.rq
	rv, ok := rq.Adapter.(Revalidator)
	if !ok || rq.Kind != Metadata || !f.haveStored || f.stored.Damaged() {
		return flightResult{}, false
	}
	stored := *f.e.document(f.stored, CacheStatus{})
	get, err := f.plainRequest(ctx)
	if err != nil {
		return resultOf(err), true
	}
	req, ok, err := rv.RevalidationRequest(ctx, UpstreamBase{URL: f.cl.Origin()}, rq.Path, rq.Variant, stored, get)
	if err != nil {
		return resultOf(asUpstreamError(err)), true
	}
	if !ok || req == nil {
		return flightResult{}, false
	}
	if err := f.cl.Allowed(req.URL); err != nil {
		return flightResult{err: err}, true
	}
	req.Header.Del("Authorization") // the client's hop policy alone adds the credential
	req.Header.Set("Accept-Encoding", "identity")
	f.target = req.URL
	resp, rcancel, idle, err := f.send(ctx, req) //nolint:bodyclose // respond or the branch below closes the body
	if err != nil {
		return f.transportFailure(ctx, err, false), true
	}
	defer rcancel()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if !rv.Unchanged(rq.Path, stored, resp) {
			_ = resp.Body.Close() // the document moved: the GET that follows fetches it, whatever a validator of the old copy says
			f.unconditional = true
			return flightResult{}, false
		}
		defer func() { _ = resp.Body.Close() }()
		return f.notModified(ctx, resp), true
	}
	return f.respond(ctx, resp, idle), true
}
