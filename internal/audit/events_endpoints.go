package audit

// The endpoint events (R-EP-016, R-EP-020, R-OPS-016): the changes of exposure
// rules. They are transactional: the producer (internal/authz, ExposureService)
// records them in the transaction of the change, after the change log raised the
// epoch, so details.epoch is the epoch of the write. An update and a deletion can
// narrow what an endpoint exposes, so both carry the revocation-journal flag from
// their registration on: a restore re-applies them (R-HA-054); the replay is M5.

// exposureStateSchema is the access-bearing state of a rule (R-EP-020).
const exposureStateSchema = `{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean"},
    "packages": {"$ref": "#/$defs/lists"},
    "paths": {"$ref": "#/$defs/lists"},
    "versions": {
      "type": "object",
      "properties": {
        "include": {"type": "array", "maxItems": 256, "items": {"type": "string"}},
        "exclude": {"type": "array", "maxItems": 256, "items": {"type": "string"}},
        "range": {"type": ["string", "null"]}
      }
    },
    "prereleases": {"type": "boolean"},
    "distTags": {"$ref": "#/$defs/lists"}
  }
}`

// exposureEventSchema is the data of the three types: the rule's endpoint, name
// and repository key, the epoch and revision of the write, the states before and
// after, the members a replacement changed, and the cause of a deletion (api, or
// repository_deleted when the repository's deletion removed the rule).
const exposureEventSchema = `{
  "type": "object",
  "$defs": {
    "lists": {
      "type": "object",
      "properties": {
        "include": {"type": "array", "maxItems": 256, "items": {"type": "string"}},
        "exclude": {"type": "array", "maxItems": 256, "items": {"type": "string"}}
      }
    },
    "state": ` + exposureStateSchema + `
  },
  "properties": {
    "endpoint": {"type": "string"},
    "name": {"type": "string"},
    "repository": {"type": "string"},
    "epoch": {"type": "integer"},
    "revision": {"type": "integer"},
    "before": {"$ref": "#/$defs/state"},
    "after": {"$ref": "#/$defs/state"},
    "changes": {"type": "array", "maxItems": 16, "items": {"type": "string"}},
    "cause": {"type": "string", "enum": ["api", "repository_deleted"]}
  },
  "required": ["endpoint", "name", "repository", "epoch"]
}`

func init() {
	Register(EventType{Name: "endpoint.exposure.created", Category: CategoryAuthz, Mode: Transactional, Schema: exposureEventSchema})
	for _, name := range []string{"endpoint.exposure.updated", "endpoint.exposure.deleted"} {
		Register(EventType{
			Name: name, Category: CategoryAuthz, Mode: Transactional,
			Journal: JournalRevocation, Schema: exposureEventSchema,
		})
	}
}
