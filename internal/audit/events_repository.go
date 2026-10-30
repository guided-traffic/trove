package audit

// The repository events (R-OPS-016, R-REPO-009 to R-REPO-013). They are
// transactional: the producer records them in the transaction of the change. None
// carries the revocation flag; a repository setting that narrows access is
// journaled by the authorization area's own authz.settings.changed event in the
// same transaction (R-AUTHZ-031).

// repositoryEventSchema is the data of the three types. changes lists the
// top-level members of the document that the write changed (key, class and format
// on a create); phase tells the two events of a deletion apart.
const repositoryEventSchema = `{
  "type": "object",
  "properties": {
    "class": {"type": "string", "enum": ["local", "remote", "virtual"]},
    "format": {"type": "string"},
    "changes": {"type": "array", "maxItems": 1000, "items": {"type": "string"}},
    "phase": {"type": "string", "enum": ["started", "completed"]}
  },
  "required": ["class", "format", "changes"],
  "additionalProperties": false
}`

// >>> M2 virtual

// claimsEventSchema is the data of repository.claims_changed (R-REPO-023): who
// changed the claims of a repository and how. source is "manual" for a claims
// write of the API and "auto" for the claim a first publish created. added lists
// the claims that were created or whose scope changed, removed the patterns that
// went; claimsVersion is the revision the change produced.
const claimsEventSchema = `{
  "type": "object",
  "properties": {
    "source": {"type": "string", "enum": ["manual", "auto"]},
    "added": {
      "type": "array", "maxItems": 1000,
      "items": {
        "type": "object",
        "properties": {
          "pattern": {"type": "string"},
          "scope": {"type": "string", "enum": ["members", "global"]}
        },
        "required": ["pattern", "scope"],
        "additionalProperties": false
      }
    },
    "removed": {"type": "array", "maxItems": 1000, "items": {"type": "string"}},
    "claimsVersion": {"type": "integer"}
  },
  "required": ["source", "added", "removed", "claimsVersion"],
  "additionalProperties": false
}`

// <<< M2 virtual

func init() {
	for _, name := range []string{"repository.created", "repository.updated", "repository.deleted"} {
		Register(EventType{
			Name:     name,
			Category: CategoryRepository,
			Mode:     Transactional,
			Schema:   repositoryEventSchema,
		})
	}
	// >>> M2 virtual
	Register(EventType{
		Name:     "repository.claims_changed",
		Category: CategoryRepository,
		Mode:     Transactional,
		Schema:   claimsEventSchema,
	})
	// <<< M2 virtual
}

// >>> M2 proxy

// EventRepositoryCacheInvalidated is the cache invalidation of a remote or virtual
// repository (R-OPS-021, R-REPO-045). path is the invalidated prefix, count the
// metadata entries it covered when the call was made.
const EventRepositoryCacheInvalidated = "repository.cache_invalidated"

func init() {
	Register(EventType{
		Name: EventRepositoryCacheInvalidated, Category: CategoryRepository, Mode: Transactional,
		Schema: `{"type":"object","properties":{"path":{"type":"string"},` +
			`"scope":{"type":"string","enum":["metadata","negative","all"]},` +
			`"count":{"type":"integer","minimum":0}},"required":["path","scope","count"],"additionalProperties":false}`,
	})
}

// <<< M2 proxy
