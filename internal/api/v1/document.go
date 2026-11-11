package v1

import (
	"bytes"
	_ "embed" // the OpenAPI document is embedded
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/guided-traffic/trove/internal/bastion/iface"
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
	// OwnerRights is x-trove-owner-rights: required. The operation acts on the caller's own account
	// (profile, preferences, sessions, the revocation of all own tokens), which no token scope names: a
	// token whose scope does not inherit its owner's rights is refused with 403 forbidden by the shared
	// step, a session and an inheriting token pass (R-API-021, R-AUTH-024, Q-API-214).
	OwnerRights bool
	// IfMatchRequired is x-trove-if-match: required (R-API-012). The operation
	// declares the shared IfMatch parameter, the generated wrapper refuses a
	// request without the header, and bindError answers it with 428
	// precondition_required (Q-API-43).
	IfMatchRequired bool
	// CIAdmitted is x-trove-ci: admitted. A CI token is admitted on the management API only on the
	// operations of the action form (repository content that needs repository actions only), on public
	// operations, and on the operations that declare this (GET and DELETE /tokens/current, R-CI-013,
	// R-CI-017). Every other operation refuses it with 403 ci_token_restricted (Q-CI-18).
	CIAdmitted bool
	// MaintenanceAllowed is x-trove-maintenance: allowed. While the installation is in read-only maintenance mode (R-OPS-024) the
	// shared step refuses every operation with a method other than GET, HEAD and OPTIONS with 503 unavailable, unless it declares
	// this: the operations that keep the installation usable (credentials, sessions, the CI exchange, cache invalidation, the
	// switch itself) and the ones that only evaluate.
	MaintenanceAllowed bool
	// BastionInterface is x-trove-bastion-interface: the interface version the operation belongs to
	// (R-BAS-008, R-BAS-009); 0 for every other operation. It is set exactly on the operations whose
	// x-trove-permission is {bastion: true}, and equals iface.InterfaceVersion.
	BastionInterface int
	// Part is x-trove-part: the part of a repository the operation serves when it is not the artifacts, "" for the artifacts (Q-API-260).
	// The only value is [PartWiki]: the operation serves the wiki part (documentation) of a repository, which is read under the wiki
	// check of R-AUTHZ-034 and not under the artifact check alone (R-ANS-037). The marker makes the seam declarative: a test lists the
	// wiki operations from the document, and the check that runs before the handler reads it.
	Part string
}

// PartWiki is the value of x-trove-part for an operation that serves the wiki part of a repository (Q-API-260).
const PartWiki = "wiki"

// Document is the parsed OpenAPI document.
type Document struct {
	root yaml.Node
	ops  map[string]Operation
	// files are the operations of the raw file path below /files/ (R-API-002):
	// they are in the document, but the generated interface leaves them out and
	// the files handler decides them (R-ARCH-011), so the authorization step of
	// the management API does not know them.
	files map[string]Operation
	// content are the operations of the UI's content routes below
	// /api/v1/projects/{projectKey}/repositories/{repoKey}/content/ (R-API-025, Q-UI-106): in the document, left out of the generated
	// interface (a path of several segments is no generated route) and decided by the files handler like /files/.
	content map[string]Operation
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
	d := &Document{ops: map[string]Operation{}, files: map[string]Operation{}, content: map[string]Operation{}}
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
			ownerRights, err := parseOwnerRights(op, req, pattern)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-owner-rights: %w", pattern, id, err))
			}
			if v, has := op["x-trove-if-match"]; has && v != "required" {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-if-match must be required, not %v", pattern, id, v))
			}
			admitted, err := parseCIAdmitted(op)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-ci: %w", pattern, id, err))
			}
			allowed, err := parseMaintenanceAllowed(op, m)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-maintenance: %w", pattern, id, err))
			}
			bastionVersion, err := parseBastionInterface(op, req)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-bastion-interface: %w", pattern, id, err))
			}
			part, err := parsePart(op)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s (%s): x-trove-part: %w", pattern, id, err))
			}
			o := Operation{
				ID: id, Pattern: pattern, Permission: perm, Requirement: req, RecentAuth: recent, OwnerRights: ownerRights,
				IfMatchRequired: op["x-trove-if-match"] == "required", CIAdmitted: admitted, MaintenanceAllowed: allowed,
				BastionInterface: bastionVersion, Part: part,
			}
			if isFilesPattern(pattern) {
				d.files[pattern] = o
			} else if isContentPattern(pattern) {
				d.content[pattern] = o
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

// parseOwnerRights reads x-trove-owner-rights of an operation: absent is false, and the only other accepted
// value is "required". It belongs to the form {authenticated: true} of the management API: an operation that
// declares a permission, a role or a repository action is decided by the decision point, which applies the
// token's scope already, and the operations of /files/ and of the content routes are decided by the files
// handler, which the shared step that enforces the attribute never wraps.
func parseOwnerRights(op map[string]any, req Requirement, pattern string) (bool, error) {
	raw, has := op["x-trove-owner-rights"]
	if !has {
		return false, nil
	}
	if v, ok := raw.(string); !ok || v != "required" {
		return false, fmt.Errorf("must be required (leave the member out on an operation that is not about the caller's own account), not %v", raw)
	}
	if isFilesPattern(pattern) || isContentPattern(pattern) {
		return false, fmt.Errorf("the files handler decides %s, and the shared step that enforces the attribute does not run there", pattern)
	}
	if !req.Authenticated {
		return false, fmt.Errorf("only an operation declared {authenticated: true} needs it; the decision point applies the token scope to %s", req)
	}
	return true, nil
}

// parseMaintenanceAllowed reads x-trove-maintenance of an operation: absent is false, and the only other accepted value is "allowed".
// It is meaningless on a method that is never refused in maintenance mode, which the document refuses.
func parseMaintenanceAllowed(op map[string]any, method string) (bool, error) {
	raw, has := op["x-trove-maintenance"]
	if !has {
		return false, nil
	}
	if v, ok := raw.(string); !ok || v != "allowed" {
		return false, fmt.Errorf("must be allowed (leave it out on an operation that is refused in maintenance mode), not %v", raw)
	}
	if !refusedInMaintenance(strings.ToUpper(method)) {
		return false, fmt.Errorf("%s is never refused in maintenance mode", strings.ToUpper(method))
	}
	return true, nil
}

// refusedInMaintenance reports whether a request with this method is a write for the read-only maintenance mode (R-OPS-024).
func refusedInMaintenance(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// parsePart reads x-trove-part of an operation: absent is "", and the only other accepted value is "wiki" (Q-API-260).
func parsePart(op map[string]any) (string, error) {
	raw, has := op["x-trove-part"]
	if !has {
		return "", nil
	}
	if raw != PartWiki {
		return "", fmt.Errorf("must be %q, not %v", PartWiki, raw)
	}
	return PartWiki, nil
}

// parseCIAdmitted reads x-trove-ci of an operation: absent is false, and the only other accepted
// value is "admitted".
func parseCIAdmitted(op map[string]any) (bool, error) {
	raw, has := op["x-trove-ci"]
	if !has {
		return false, nil
	}
	if v, ok := raw.(string); ok && v == "admitted" {
		return true, nil
	}
	return false, fmt.Errorf("must be admitted (leave the member out to refuse CI tokens), not %v", raw)
}

// parseBastionInterface reads x-trove-bastion-interface of an operation. The member and the permission form
// {bastion: true} go together, and the member's value is the interface version of this build: an operation
// documented for another version is a change of the interface that the guard of R-BAS-009 did not see.
func parseBastionInterface(op map[string]any, req Requirement) (int, error) {
	raw, has := op["x-trove-bastion-interface"]
	switch {
	case !has && !req.Bastion:
		return 0, nil
	case !has:
		return 0, fmt.Errorf("the operation is declared {bastion: true} but names no interface version")
	case !req.Bastion:
		return 0, fmt.Errorf("only an operation declared {bastion: true} belongs to the Bastion interface")
	}
	v, ok := raw.(int)
	if !ok || v != iface.InterfaceVersion {
		return 0, fmt.Errorf("is %v, the interface version of this build is %d (R-BAS-009)", raw, iface.InterfaceVersion)
	}
	return v, nil
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

// ContentOperations returns the operations of the UI's content routes
// (/api/v1/projects/{projectKey}/repositories/{repoKey}/content/{path}) keyed by route pattern, with the {path} of the document.
func (d *Document) ContentOperations() map[string]Operation {
	out := make(map[string]Operation, len(d.content))
	for k, v := range d.content {
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
		case isContentPattern(p):
			n := documentedFilesPattern(p)
			have[n] = true
			if _, ok := d.content[n]; !ok {
				problems = append(problems, fmt.Errorf("route %q is registered but not in api/openapi.yaml", p))
			}
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
	for _, ops := range []map[string]Operation{d.ops, d.files, d.content} {
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

// isContentPattern reports whether a ServeMux pattern or a documented path is one of the UI's content routes.
func isContentPattern(p string) bool {
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = strings.TrimSpace(rest)
	}
	return strings.HasPrefix(p, "/api/v1/projects/{projectKey}/repositories/{repoKey}/content/")
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
