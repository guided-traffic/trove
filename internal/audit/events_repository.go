package audit

// The repository events (R-OPS-016, R-REPO-009 to R-REPO-013). They are
// transactional: the producer records them in the transaction of the change. An
// authorization setting that narrows access is journaled by the authorization
// area's own authz.settings.changed event in the same transaction (R-AUTHZ-031).
// Two repository events end all access to a repository and carry the revocation
// flag themselves (Q-AUTHZ-238, Q-AUTHZ-239): repository.disabled, which an
// update records next to repository.updated when it sets disabled, and
// repository.deleted. Their entry is the repository's ID and qualified name; the
// replay disables the repository.

// repositoryEventSchema is the data of the three types. changes lists the
// top-level members of the document that the write changed (key, class and format
// on a create); phase tells the two events of a deletion apart; project is the key of
// the repository's project, absent for a repository of none (R-AUTHZ-026).
const repositoryEventSchema = `{
  "type": "object",
  "properties": {
    "class": {"type": "string", "enum": ["local", "remote", "virtual"]},
    "format": {"type": "string"},
    "changes": {"type": "array", "maxItems": 1000, "items": {"type": "string"}},
    "phase": {"type": "string", "enum": ["started", "completed"]},
    "project": {"type": "string"}
  },
  "required": ["class", "format", "changes"],
  "additionalProperties": false
}`

// >>> M2 virtual

// claimsEventSchema is the data of repository.claims_changed (R-REPO-023): who
// changed the claims of a repository and how. source is "manual" for a claims
// write of the API and "auto" for the claim a first publish created. added lists
// the claims that were created or whose scope changed, removed the patterns that
// went; claimsVersion is the revision the change produced. The event is journaled
// (R-REPO-061, Q-REPO-250): its entry carries the created and widened claims.
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

// EventRepositoryDisabled is an update that set disabled on a repository
// (R-REPO-004): the repository takes no part in resolution, download or deploy
// any more, so the event is journaled (R-AUTHZ-031, Q-AUTHZ-238).
const EventRepositoryDisabled = "repository.disabled"

// EventRepositoryNarrowed is an update that narrowed what a repository serves (R-REPO-061, Q-REPO-245): fewer include
// patterns, more exclude patterns, a virtual member taken out, a pin rule, auto claim or stronger vulnerabilities member.
// An update records it next to repository.updated, in the same transaction, and only when it narrows. Its journal entry
// carries the parts that narrowed; the details name them in parts.
const EventRepositoryNarrowed = "repository.narrowed"

func init() {
	for name, journal := range map[string]Journal{
		"repository.created": NoJournal, "repository.updated": NoJournal, "repository.deleted": JournalRevocation,
	} {
		Register(EventType{
			Name:     name,
			Category: CategoryRepository,
			Mode:     Transactional,
			Journal:  journal,
			Schema:   repositoryEventSchema,
		})
	}
	Register(EventType{
		Name: EventRepositoryDisabled, Category: CategoryRepository, Mode: Transactional, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{"class":{"type":"string","enum":["local","remote","virtual"]},` +
			`"format":{"type":"string"},"project":{"type":"string"}},"required":["class","format"],"additionalProperties":false}`,
	})
	Register(EventType{
		Name: EventRepositoryNarrowed, Category: CategoryRepository, Mode: Transactional, Journal: JournalRevocation,
		Schema: `{"type":"object","properties":{"class":{"type":"string","enum":["local","remote","virtual"]},` +
			`"format":{"type":"string"},"project":{"type":"string"},` +
			`"parts":{"type":"array","maxItems":16,"items":{"type":"string"}}},` +
			`"required":["class","format","parts"],"additionalProperties":false}`,
	})
	// >>> M2 virtual
	Register(EventType{
		Name:     "repository.claims_changed",
		Category: CategoryRepository,
		Mode:     Transactional,
		Journal:  JournalRevocation,
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

// >>> M2 npm

// EventRepositoryRemotePackageShadowed is the security event of a first publish of a
// name that a remote member of a containing virtual repository serves, made on the
// strength of manage (R-REPO-048). The local repository is the event's repository,
// package the name, exposure the pairs (virtual, remote) that serve it.
const EventRepositoryRemotePackageShadowed = "repository.remote_package_shadowed"

func init() {
	Register(EventType{
		Name: EventRepositoryRemotePackageShadowed, Category: CategoryRepository, Mode: Transactional,
		Schema: `{"type":"object","properties":{"package":{"type":"string"},` +
			`"exposure":{"type":"array","maxItems":1000,"items":{"type":"object","properties":{` +
			`"virtual":{"type":"string"},"remote":{"type":"string"}},"required":["virtual","remote"],"additionalProperties":false}}},` +
			`"required":["package","exposure"],"additionalProperties":false}`,
	})
}

// <<< M2 npm
