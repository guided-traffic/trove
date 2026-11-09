package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// ownerRightsOperations are the operations on the caller's own account that declare x-trove-owner-rights: required
// (R-API-021, Q-API-214).
var ownerRightsOperations = []string{
	"endOwnSession", "endOwnSessions", "listOwnSessions", "removeAvatar", "revokeOwnTokens", "setAvatar", "updatePreferences",
}

// ownerRightsExempt are the operations with an unsafe method that admit every authenticated caller and do not declare
// x-trove-owner-rights, each with what bounds a token whose scope does not inherit its owner's rights there instead. A new
// such operation either declares the attribute or is added here with its reason, read in its handler.
var ownerRightsExempt = map[string]string{
	"putDebCoordinates": "decides deploy for every added coordinate and delete for every removed one on the file's package, with the request's " +
		"subject and scope, through the decision point (R-DEB-004, R-DEB-009)",
	"addPackageOwner": "the ownership service decides on the request's subject: manage on the name and content:bypass through the " +
		"decision point with the scope, and the owner path for a token that does not inherit only where its scope lists manage on " +
		"the name (scopeReachesName, Q-AUTHZ-240)",
	"removePackageOwner": "as addPackageOwner",
	"cancelJob": "settings:write through the decision point, with the scope; the starter's path admits a session and an " +
		"inheriting token only (mayCancel, Q-API-215)",
	"checkPermissions":                 "computes the answers for the presenting credential, its scope included; writes nothing",
	"validatePermissionPolicy":         "validates a policy document for a caller the decision point admits (mayValidate); writes nothing",
	"createToken":                      "the derivation rule (R-AUTH-027): the new token is derived from the presenting one, within its scope and expiry",
	"createServiceAccountToken":        "an administrator through the decision point, or a token of the account under the derivation rule (R-AUTH-027)",
	"createProjectServiceAccountToken": "an administrator or an Owner of the project through the decision point, or a token of the account under the derivation rule",
	"revokeToken": "the line of a narrowed token (R-AUTH-029, Q-AUTH-199): by ownership it sees and revokes only itself and its " +
		"line, another token of its owner is a 404 to it; beyond that tokens:revoke through the decision point or an Owner of the " +
		"account's project",
	"revokeCurrentToken": "acts on the presenting token itself (R-AUTH-029)",
	"rotateToken": "the line of a narrowed token and the derivation rule (R-AUTH-030, Q-AUTH-199): it rotates only itself and its " +
		"line, and the successor stays within the presenting token's scope and expiry; or, for a service-account token, an " +
		"administrator through the decision point with the scope (a narrowed token only with admin: true), whose rotation has no " +
		"parent and so no derivation limits, as an administrator's session (rotationParent)",
	"rotateCurrentToken":  "acts on the presenting token itself, only if it was created selfRotatable (R-AUTH-030)",
	"runRepositoryMirror": "repositories:write-config or an Owner of the project, both through the decision point with the scope",
	"revokeBastionToken":  "endpoints:write or tokens:revoke through the decision point, with the scope (requireAny)",
}

// ownerRightsReads are the authenticated reads (GET) that do not declare x-trove-owner-rights, each with why a token whose
// scope does not inherit its owner's rights may call it: what it returns is the credential's own, is filtered with the
// token's scope, or holds nothing of the owner's account. A new authenticated read either declares the attribute or is
// added here with its reason, read in its handler; listOwnSessions declares it.
var ownerRightsReads = map[string]string{
	"getCurrentUser":  "the own user record, which the credential may know about its principal and the UI of a restricted token needs (Q-API-214)",
	"getPreferences":  "the own UI preferences, read only; the UI of a restricted token needs them (Q-API-214)",
	"getWhoami":       "the principal and the presenting credential itself (R-AUTH-028)",
	"getAuthzMe":      "the capabilities and memberships the decision point computes for the presenting credential, its scope included (R-AUTHZ-022)",
	"getCurrentToken": "the presenting token itself (R-AUTH-028)",
	"getSystemMaintenance": "the installation's maintenance mode, which holds nothing of the owner's account; every client that is refused a " +
		"write reads it to learn why (R-OPS-024)",
	"listTokens": "a narrowed token lists only its line, itself and the tokens derived from it or from its rotation predecessors; " +
		"more only with tokens:read through the decision point (ListFilter.Line, Q-AUTH-199)",
	"getToken": "a narrowed token sees only its line; any other token of its owner is a 404 unless tokens:read admits it " +
		"(inLine in visible, Q-AUTH-199)",
	"getJob": "settings:read through the decision point, or the starter's own job; the read stays open so a token polls the run " +
		"it started (visibleJobs, Q-API-215)",
	"listJobs":                         "as getJob (visibleJobs, Q-API-215)",
	"listOwnedPackages":                "policies:read through the decision point, or the caller's own identity, then filtered to the repositories the subject reads with its scope (OwnedBy, ReadableOn)",
	"listRoles":                        "the fixed catalogue of the system roles; nothing of an account",
	"getVersion":                       "the build facts of the replica; nothing of an account",
	"getAvatar":                        "an image by its digest, readable by every authenticated caller (Q-AUTH-195); nothing of the caller's account",
	"getRepositoryMirror":              "decided through the decision point with the scope (requireMirrorReader)",
	"listCacheEntries":                 "decided through the decision point with the scope (requireCacheReader)",
	"getVulnerabilitySummary":          "counts over the repositories the read scope of the decision point admits for the subject, scope included (vulnCaller, ReadScopeOn)",
	"getVulnerabilityTrend":            "as getVulnerabilitySummary",
	"listVulnerabilityFindings":        "as getVulnerabilitySummary",
	"getVulnerabilityFinding":          "as getVulnerabilitySummary; a finding outside the read scope is a 404",
	"exportVulnerabilityFindings":      "as getVulnerabilitySummary",
	"getVulnerabilityAdvisory":         "the advisory and the findings of it inside the subject's read scope (report.Scope)",
	"getPackageVulnerabilities":        "the findings of a package inside the subject's read scope (report.PackageScope)",
	"getPackageVersionVulnerabilities": "as getPackageVulnerabilities",
}

func loadDoc(t *testing.T) *Document {
	t.Helper()
	doc, err := LoadDocument(EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// opByID returns the operation of the management API with the operationId.
func opByID(t *testing.T, doc *Document, id string) Operation {
	t.Helper()
	for _, op := range doc.Operations() {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("no operation %s", id)
	return Operation{}
}

func TestR_API_021_DocumentDeclaresOwnerRights(t *testing.T) {
	doc := loadDoc(t)
	t.Run("R-API-021 exactly the operations on the caller's own account declare x-trove-owner-rights", func(t *testing.T) {
		var got []string
		for _, op := range doc.Operations() {
			if op.OwnerRights {
				got = append(got, op.ID)
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(ownerRightsOperations, ",") {
			t.Fatalf("declared:\n%v\nwant:\n%v", got, ownerRightsOperations)
		}
	})
	t.Run("R-API-021 x-trove-owner-rights: optional is refused at load", func(t *testing.T) {
		raw := strings.Replace(string(EmbeddedYAML()), "x-trove-owner-rights: required", "x-trove-owner-rights: optional", 1)
		if _, err := LoadDocument([]byte(raw)); err == nil || !strings.Contains(err.Error(), "x-trove-owner-rights") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("R-API-021 the attribute belongs to the form authenticated: true", func(t *testing.T) {
		op := map[string]any{"x-trove-owner-rights": "required"}
		if _, err := parseOwnerRights(op, Requirement{Permission: "identities:write"}, "PATCH /api/v1/users/{username}"); err == nil {
			t.Fatal("a permission declaration with x-trove-owner-rights was accepted")
		}
		if v, err := parseOwnerRights(op, Requirement{Authenticated: true}, "PATCH /api/v1/users/me/preferences"); err != nil || !v {
			t.Fatalf("%v %v", v, err)
		}
		if v, err := parseOwnerRights(map[string]any{}, Requirement{Authenticated: true}, "PATCH /api/v1/users/me/preferences"); err != nil || v {
			t.Fatalf("an absent attribute: %v %v", v, err)
		}
	})
}

func TestR_API_021_EveryAuthenticatedWriteDeclaresItsTokenRule(t *testing.T) {
	doc := loadDoc(t)
	ids := map[string]Operation{}
	for pattern, op := range doc.Operations() {
		ids[op.ID] = op
		method, _, _ := strings.Cut(pattern, " ")
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || !op.Requirement.Authenticated {
			continue
		}
		t.Run("R-API-021 "+op.ID, func(t *testing.T) {
			reason, exempt := ownerRightsExempt[op.ID]
			switch {
			case op.OwnerRights && exempt:
				t.Fatalf("%s declares x-trove-owner-rights and is in ownerRightsExempt (%s)", op.ID, reason)
			case !op.OwnerRights && !exempt:
				t.Fatalf("%s (%s) admits every authenticated caller with an unsafe method: declare x-trove-owner-rights: required "+
					"if it acts on the caller's own account, or add it to ownerRightsExempt with the rule that bounds a narrowed token", op.ID, pattern)
			}
		})
	}
	t.Run("R-API-021 every exempt entry is an unsafe authenticated operation", func(t *testing.T) {
		for id := range ownerRightsExempt {
			op, ok := ids[id]
			if !ok {
				t.Errorf("%s is not an operation of the document", id)
				continue
			}
			method, _, _ := strings.Cut(op.Pattern, " ")
			if method == http.MethodGet || !op.Requirement.Authenticated {
				t.Errorf("%s (%s, %s) needs no entry", id, op.Pattern, op.Requirement)
			}
		}
	})
}

func TestR_API_021_EveryAuthenticatedReadIsClassified(t *testing.T) {
	doc := loadDoc(t)
	ids := map[string]Operation{}
	for pattern, op := range doc.Operations() {
		ids[op.ID] = op
		method, _, _ := strings.Cut(pattern, " ")
		if (method != http.MethodGet && method != http.MethodHead) || !op.Requirement.Authenticated {
			continue
		}
		t.Run("R-API-021 "+op.ID, func(t *testing.T) {
			reason, listed := ownerRightsReads[op.ID]
			switch {
			case op.OwnerRights && listed:
				t.Fatalf("%s declares x-trove-owner-rights and is in ownerRightsReads (%s)", op.ID, reason)
			case !op.OwnerRights && !listed:
				t.Fatalf("%s (%s) is an authenticated read: declare x-trove-owner-rights: required if it returns the caller's own "+
					"account beyond what the credential may know about itself, or add it to ownerRightsReads with the reason", op.ID, pattern)
			}
		})
	}
	t.Run("R-API-021 every listed read is an authenticated GET of the document", func(t *testing.T) {
		for id := range ownerRightsReads {
			op, ok := ids[id]
			if !ok {
				t.Errorf("%s is not an operation of the document", id)
				continue
			}
			if method, _, _ := strings.Cut(op.Pattern, " "); method != http.MethodGet || !op.Requirement.Authenticated {
				t.Errorf("%s (%s, %s) needs no entry", id, op.Pattern, op.Requirement)
			}
		}
	})
}

func TestR_API_021_TheAttributeIsRefusedOnTheOperationsOfTheFilesHandler(t *testing.T) {
	for _, id := range []string{"deleteFile", "deployContent"} {
		t.Run("R-API-021 "+id, func(t *testing.T) {
			marker := "      operationId: " + id + "\n"
			raw := string(EmbeddedYAML())
			if !strings.Contains(raw, marker) {
				t.Fatalf("no operation %s", id)
			}
			raw = strings.Replace(raw, marker, marker+"      x-trove-owner-rights: required\n", 1)
			_, err := LoadDocument([]byte(raw))
			if err == nil || !strings.Contains(err.Error(), "x-trove-owner-rights") || !strings.Contains(err.Error(), "files handler") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// owners are the callers the gate is asked about: who presents what.
func owners() map[string]httpserver.Principal {
	user := func(c httpserver.Credential) httpserver.Principal {
		return httpserver.Principal{Kind: httpserver.KindUser, ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Name: "alice", Credential: c}
	}
	tok := func(sc scope.Scope) httpserver.Credential {
		return httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a11", TokenType: httpserver.TokenPAT, Scope: &sc}
	}
	return map[string]httpserver.Principal{
		"a token limited to one capability":           user(tok(scope.Scope{System: []string{"settings:read"}})),
		"a token limited to one repository":           user(tok(scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"it/npm-local"}, Actions: []string{"read"}}}})),
		"a token with admin: true and inherit: false": user(tok(scope.Scope{Admin: true})),
		"a token that inherits the owner's rights":    user(tok(scope.Scope{Inherit: true})),
		"a token that inherits, bound to an endpoint": user(tok(scope.Scope{Inherit: true, Endpoints: []string{"default"}})),
		"a session": user(httpserver.Credential{Method: httpserver.MethodSession, SessionID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a12"}),
		"the break-glass session": {
			Kind: httpserver.KindBreakGlass, ID: "break-glass", Name: "admin",
			Credential: httpserver.Credential{Method: httpserver.MethodSession, SessionID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a13"},
		},
		"an anonymous caller": {Anonymous: true, Kind: httpserver.KindAnonymous},
	}
}

// refusedByTheGate is the set of owners above whose request the gate refuses.
var refusedByTheGate = map[string]bool{
	"a token limited to one capability": true, "a token limited to one repository": true, "a token with admin: true and inherit: false": true,
}

// requestFor builds a request for an operation of the document: every path parameter is a UUID.
func requestFor(op Operation) *http.Request {
	method, path, _ := strings.Cut(op.Pattern, " ")
	for strings.Contains(path, "{") {
		i, j := strings.Index(path, "{"), strings.Index(path, "}")
		path = path[:i] + "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a20" + path[j+1:]
	}
	return httptest.NewRequest(method, path, strings.NewReader("{}"))
}

// throughTheStep sends the request of op through the authorization step alone, with a handler that records whether it
// ran in place of the operation's handler.
func throughTheStep(t *testing.T, doc *Document, op Operation, p httpserver.Principal) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	h := New(Options{Document: doc, Access: allowAll{}}).authorize(op.Pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	r := requestFor(op)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r.WithContext(httpserver.WithPrincipal(r.Context(), p)))
	return w, reached
}

type problemBody struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func gateProblem(t *testing.T, w *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return p
}

func TestR_API_021_ANarrowedTokenIsRefusedOnTheOperationsOfTheOwnAccount(t *testing.T) {
	doc := loadDoc(t)
	for _, id := range ownerRightsOperations {
		op := opByID(t, doc, id)
		t.Run("R-API-021 "+id, func(t *testing.T) {
			for name, p := range owners() {
				w, reached := throughTheStep(t, doc, op, p)
				switch {
				case p.Anonymous:
					if w.Code != http.StatusUnauthorized || reached {
						t.Errorf("%s: %d, handler reached %v, want 401 before the handler", name, w.Code, reached)
					}
				case refusedByTheGate[name]:
					if pr := gateProblem(t, w); w.Code != http.StatusForbidden || pr.Code != "forbidden" || pr.Detail != ownerRightsRefusal || reached {
						t.Errorf("%s: %d %+v, handler reached %v, want the gate's 403 forbidden", name, w.Code, pr, reached)
					}
				default:
					if !reached {
						t.Errorf("%s: %d %s, the handler was not reached", name, w.Code, w.Body.String())
					}
				}
			}
		})
	}
}

// TestR_API_021_TheRegisteredRoutesCarryTheGate sends the requests through the chain and the router New registers, so the
// generated wrapper and the handler sit behind the step as in production: a narrowed token meets the gate, the other callers
// reach the handler, which answers for itself (here 503 or 428: the setup has no stores).
func TestR_API_021_TheRegisteredRoutesCarryTheGate(t *testing.T) {
	for _, id := range ownerRightsOperations {
		t.Run("R-API-021 "+id, func(t *testing.T) {
			for name, p := range owners() {
				if p.Anonymous {
					continue
				}
				s := newSetup(t, nil, func(*http.Request) (httpserver.Principal, error) { return p, nil })
				r := requestFor(opByID(t, s.doc, id))
				if id == "updatePreferences" {
					r.Header.Set("Content-Type", "application/merge-patch+json")
				}
				w := httptest.NewRecorder()
				s.handler.ServeHTTP(w, r)
				gate := w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), ownerRightsRefusal)
				if gate != refusedByTheGate[name] {
					t.Errorf("%s: %d %s, refused by the gate %v, want %v", name, w.Code, w.Body.String(), gate, refusedByTheGate[name])
				}
			}
		})
	}
	t.Run("R-API-021 updatePreferences without If-Match: the gate answers before the 428 of the wrapper", func(t *testing.T) {
		for name, p := range owners() {
			if p.Anonymous {
				continue
			}
			s := newSetup(t, nil, func(*http.Request) (httpserver.Principal, error) { return p, nil })
			r := httptest.NewRequest(http.MethodPatch, "/api/v1/users/me/preferences", strings.NewReader(`{"theme":"dark"}`))
			r.Header.Set("Content-Type", "application/merge-patch+json")
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, r)
			want := http.StatusPreconditionRequired
			if refusedByTheGate[name] {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), want)
			}
		}
	})
}

func TestR_API_021_TheReadsOfTheOwnProfileAdmitANarrowedToken(t *testing.T) {
	doc := loadDoc(t)
	for _, id := range []string{"getCurrentUser", "getPreferences", "getWhoami", "getAuthzMe"} {
		op := opByID(t, doc, id)
		t.Run("R-API-021 "+id, func(t *testing.T) {
			if op.OwnerRights {
				t.Fatalf("%s declares x-trove-owner-rights", id)
			}
			for name, p := range owners() {
				if !refusedByTheGate[name] {
					continue
				}
				if w, reached := throughTheStep(t, doc, op, p); !reached {
					t.Errorf("%s: %d %s, the handler was not reached", name, w.Code, w.Body.String())
				}
			}
		})
	}
}
