// Package apitest validates the responses of the management API against
// api/openapi.yaml (R-API-002 CI item 4, exit criterion E1.6). It is test
// support: no production package imports it.
//
// A test wraps the handler under test in [Validator.Handler] (or checks a single
// answer with [Validator.Check]); every answer is then matched to its operation
// by method and path, and its status, headers and body are validated against the
// operation's response definition, with undeclared statuses failing. At the end
// of the test run [Validator.Operations] says which operations were exercised, so
// a test can fail on an operation that no test reached.
//
// The validator is kin-openapi, which supports OpenAPI 3.1 and JSON Schema 2020-12
// (type arrays with "null", oneOf with a null branch); the construct tests in
// this package pin what it handles.
package apitest

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// LoadDocument parses spec and validates it as an OpenAPI 3.1 document: the
// version, the structure and every reference. It returns the document with
// servers removed, so a request matches by path alone.
func LoadDocument(spec []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(spec)
	if err != nil {
		return nil, fmt.Errorf("loading the OpenAPI document: %w", err)
	}
	if !doc.IsOpenAPI31OrLater() {
		return nil, fmt.Errorf("the document is OpenAPI %s, want 3.1", doc.OpenAPI)
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("the OpenAPI document is not valid: %w", err)
	}
	doc.Servers = nil
	return doc, nil
}

// Validator checks responses against one document.
type Validator struct {
	doc    *openapi3.T
	router routers.Router

	mu   sync.Mutex
	seen map[string]map[int]bool // operationId -> statuses answered
}

// New builds a validator for spec (the bytes of api/openapi.yaml, for example
// v1.EmbeddedYAML()); the test fails when the document is not a valid 3.1
// document.
func New(t testing.TB, spec []byte) *Validator {
	t.Helper()
	doc, err := LoadDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("building the OpenAPI router: %v", err)
	}
	return &Validator{doc: doc, router: router, seen: map[string]map[int]bool{}}
}

// Check validates one answer: req is the request that was sent (method and URL
// matter), status, header and body the answer. It returns the operationId of the
// route and the first violation.
func (v *Validator) Check(req *http.Request, status int, header http.Header, body []byte) (string, error) {
	route, params, err := v.router.FindRoute(req)
	if err != nil {
		return "", fmt.Errorf("%s %s is not an operation of the document: %w", req.Method, req.URL.Path, err)
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route},
		Status:                 status,
		Header:                 header,
		Options:                &openapi3filter.Options{IncludeResponseStatus: true, MultiError: true},
	}
	in.SetBodyBytes(body)
	id := route.Operation.OperationID
	v.mu.Lock()
	if v.seen[id] == nil {
		v.seen[id] = map[int]bool{}
	}
	v.seen[id][status] = true
	v.mu.Unlock()
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		return id, fmt.Errorf("%s %s answered %d: %w", req.Method, req.URL.Path, status, err)
	}
	return id, nil
}

// Handler wraps next: every answer is validated, and a violation fails t. The
// answer reaches the client unchanged.
func (v *Validator) Handler(t testing.TB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &capture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// The request as the client sent it: the chain may have replaced r.Pattern.
		if _, err := v.Check(r, rec.status, rec.Header().Clone(), rec.body.Bytes()); err != nil {
			t.Errorf("response does not match api/openapi.yaml: %v", err)
		}
	})
}

// Recorder returns a recorder and a function that validates what it recorded.
func (v *Validator) Recorder(req *http.Request) (*httptest.ResponseRecorder, func() error) {
	rec := httptest.NewRecorder()
	return rec, func() error {
		res := rec.Result()
		_, err := v.Check(req, res.StatusCode, res.Header, rec.Body.Bytes())
		return err
	}
}

// Operations returns the operationIds the validator saw answered, sorted, with
// the statuses seen.
func (v *Validator) Operations() map[string][]int {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string][]int, len(v.seen))
	for id, set := range v.seen {
		for st := range set {
			out[id] = append(out[id], st)
		}
		sort.Ints(out[id])
	}
	return out
}

// Documented lists the operationIds of the document, sorted.
func (v *Validator) Documented() []string {
	var ids []string
	for _, item := range v.doc.Paths.Map() {
		for _, op := range item.Operations() {
			ids = append(ids, op.OperationID)
		}
	}
	sort.Strings(ids)
	return ids
}

// Uncovered returns the documented operations among want that no answer
// reached; a test that owns operations asserts it is empty.
func (v *Validator) Uncovered(want ...string) []string {
	seen := v.Operations()
	var out []string
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// capture copies what a handler writes.
type capture struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (c *capture) WriteHeader(status int) {
	if !c.wroteHeader {
		c.status, c.wroteHeader = status, true
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *capture) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.wroteHeader = true
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (c *capture) Unwrap() http.ResponseWriter { return c.ResponseWriter }
