package v1

import (
	"bytes"
	_ "embed" // the OpenAPI document is embedded
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// specYAML is the embedded copy of api/openapi.yaml. `go generate` refreshes it,
// and TestEmbeddedCopyMatchesSource fails when it is stale.
//
//go:embed openapi.yaml
var specYAML []byte

// EmbeddedYAML returns the embedded document exactly as committed.
func EmbeddedYAML() []byte { return bytes.Clone(specYAML) }

// Operation is one operation of the document with the declarations the
// authorization step reads (R-API-002, R-API-008).
type Operation struct {
	ID      string
	Pattern string // the ServeMux pattern, for example "GET /api/v1/system/ping"
	// Permission is the value of x-trove-permission, decoded.
	Permission map[string]any
	// Requirement is Permission in the form the authorization step enforces.
	Requirement Requirement
	// RecentAuth is x-trove-recent-auth: true. A caller that authenticates with a
	// session must have signed in at the identity provider within
	// auth.sessions.recent_auth_window, otherwise the shared step answers 401
	// reauthentication_required (R-API-007, R-AUTH-011). A token is not asked.
	RecentAuth bool
	// IfMatchRequired is x-trove-if-match: required (R-API-012). The operation
	// declares the shared IfMatch parameter, the generated wrapper refuses a
	// request without the header, and bindError answers it with 428
	// precondition_required (Q-API-43).
	IfMatchRequired bool
}

// Document is the parsed OpenAPI document.
type Document struct {
	root yaml.Node
	ops  map[string]Operation
	// files are the operations of the raw file path below /files/ (R-API-002):
	// they are in the document, but the generated interface leaves them out and
	// the files handler decides them (R-ARCH-011), so the authorization step of
	// the management API does not know them.
	files map[string]Operation
}

// httpMethods are the operation keys of a path item.
func httpMethods() []string {
	return []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}
}

// LoadDocument parses a document and builds the table of operations keyed by
// route pattern. Every operation must carry an operationId and an
// x-trove-permission declaration in a form the authorization step enforces: no
// operation can run without a declared and enforced permission (R-API-002,
// R-API-008).
func LoadDocument(raw []byte) (*Document, error) {
	d := &Document{ops: map[string]Operation{}, files: map[string]Operation{}}
	if err := yaml.Unmarshal(raw, &d.root); err != nil {
		return nil, fmt.Errorf("openapi document: %w", err)
	}
	var tree map[string]any
	if err := d.root.Decode(&tree); err != nil {
		return nil, fmt.Errorf("openapi document: %w", err)
	}
	if v, _ := tree["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		return nil, fmt.Errorf("openapi document: openapi is %q, want 3.1.x", v)
	}
	paths, _ := tree["paths"].(map[string]any)
	var problems []error
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for _, m := range httpMethods() {
			opv, ok := methods[m]
			if !ok {
				continue
			}
			op, _ := opv.(map[string]any)
			pattern := strings.ToUpper(m) + " " + path
			id, _ := op["operationId"].(string)
			if id == "" {
				problems = append(problems, fmt.Errorf("%s has no operationId", pattern))
			}
			perm, _ := op["x-trove-permission"].(map[string]any)
			var req Requirement
			if len(perm) == 0 {
				problems = append(problems, fmt.Errorf("%s (%s) has no x-trove-permission declaration", pattern, id))
			} else if r, err := parseRequirement(perm); err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-permission: %w", pattern, id, err))
			} else {
				req = r
			}
			recent, err := parseRecentAuth(op)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-recent-auth: %w", pattern, id, err))
			}
			if v, has := op["x-trove-if-match"]; has && v != "required" {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-if-match must be required, not %v", pattern, id, v))
			}
			o := Operation{ID: id, Pattern: pattern, Permission: perm, Requirement: req, RecentAuth: recent, IfMatchRequired: op["x-trove-if-match"] == "required"}
			if isFilesPattern(pattern) {
				d.files[pattern] = o
			} else {
				d.ops[pattern] = o
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, fmt.Errorf("openapi document: %w", err)
	}
	return d, nil
}

// parseRecentAuth reads x-trove-recent-auth of an operation: absent is false, and
// the only other accepted value is true (leave the member out instead of false).
func parseRecentAuth(op map[string]any) (bool, error) {
	raw, has := op["x-trove-recent-auth"]
	if !has {
		return false, nil
	}
	if v, ok := raw.(bool); ok && v {
		return true, nil
	}
	return false, fmt.Errorf("must be true (leave it out instead of %v)", raw)
}

// FileOperations returns the operations of the raw file path (/files/...) keyed
// by route pattern, with the {path} of the document.
func (d *Document) FileOperations() map[string]Operation {
	out := make(map[string]Operation, len(d.files))
	for k, v := range d.files {
		out[k] = v
	}
	return out
}

// Operations returns the table of the management API's operations (below
// /api/v1/) keyed by route pattern.
func (d *Document) Operations() map[string]Operation {
	out := make(map[string]Operation, len(d.ops))
	for k, v := range d.ops {
		out[k] = v
	}
	return out
}

// CheckRoutes compares the registered patterns below /api/v1/ with the
// document's operations in both directions (R-API-002 CI item 5, enforced at
// start-up): a registered route without an entry, and a documented operation
// without a route, stop start-up and are named.
func (d *Document) CheckRoutes(registered []string) error {
	have := map[string]bool{}
	var problems []error
	for _, p := range registered {
		switch {
		case isV1Pattern(p):
			have[p] = true
			if _, ok := d.ops[p]; !ok {
				problems = append(problems, fmt.Errorf("route %q is registered but not in api/openapi.yaml", p))
			}
		case isFilesPattern(p):
			n := documentedFilesPattern(p)
			have[n] = true
			if _, ok := d.files[n]; !ok {
				problems = append(problems, fmt.Errorf("route %q is registered but not in api/openapi.yaml", p))
			}
		}
	}
	var missing []string
	for _, ops := range []map[string]Operation{d.ops, d.files} {
		for p := range ops {
			if !have[p] {
				missing = append(missing, p)
			}
		}
	}
	sort.Strings(missing)
	for _, p := range missing {
		problems = append(problems, fmt.Errorf("operation %q is in api/openapi.yaml but has no route", p))
	}
	return errors.Join(problems...)
}

// isV1Pattern reports whether a ServeMux pattern lies below /api/v1/.
func isV1Pattern(p string) bool {
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = strings.TrimSpace(rest)
	}
	return strings.HasPrefix(p, "/api/v1/")
}

// isFilesPattern reports whether a ServeMux pattern lies below /files/.
func isFilesPattern(p string) bool {
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = strings.TrimSpace(rest)
	}
	return strings.HasPrefix(p, "/files/")
}

// documentedFilesPattern turns the route of the files handler into the template
// of the document: the multi-segment wildcard {path...} is the parameter {path}.
func documentedFilesPattern(p string) string {
	return strings.Replace(p, "{path...}", "{path}", 1)
}

// withServer returns a copy of the document tree whose servers[0].url is baseURL.
func (d *Document) withServer(baseURL string) (*yaml.Node, error) {
	var n yaml.Node
	b, err := yaml.Marshal(&d.root)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	if baseURL == "" {
		return &n, nil
	}
	root := &n
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	servers := mapValue(root, "servers")
	if servers == nil || servers.Kind != yaml.SequenceNode || len(servers.Content) == 0 {
		return &n, nil
	}
	if url := mapValue(servers.Content[0], "url"); url != nil {
		url.Value = baseURL
	}
	return &n, nil
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// YAML renders the document with servers[0].url set to baseURL (R-API-002).
func (d *Document) YAML(baseURL string) ([]byte, error) {
	n, err := d.withServer(baseURL)
	if err != nil {
		return nil, err
	}
	return yaml.Marshal(n)
}

// JSON renders the document as JSON with servers[0].url set to baseURL.
func (d *Document) JSON(baseURL string) ([]byte, error) {
	n, err := d.withServer(baseURL)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := n.Decode(&tree); err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}
