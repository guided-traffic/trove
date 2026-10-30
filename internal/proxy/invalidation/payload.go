package invalidation

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// PayloadVersion is the version of the cache_invalidation change payloads this
// build writes and understands (cluster.Area.PayloadVersion).
const PayloadVersion = 1

// Kinds of the cache_invalidation payload (Q-OPS-154). A consumer of the area that
// does not know a kind ignores it; the Set reloads on one it does not know.
const (
	// KindAdd announces a new row of a remote repository. It carries the whole row,
	// so a replica applies it without a query.
	KindAdd = "add"
	// KindDelete announces that the fold task deleted rows of a remote repository;
	// the replica reloads the repository's rows from PostgreSQL.
	KindDelete = "del"
	// KindVirtual announces the invalidation of a virtual repository: the merged
	// entries under the prefix are dropped (R-REPO-020). No row exists.
	KindVirtual = "virt"
)

// Payload is the change payload of the area cache_invalidation. The prefix travels
// as unpadded base64url, because json.Marshal escapes <, > and & to six bytes
// each and the payload has a hard limit of 1 KiB.
type Payload struct {
	Kind string `json:"k"`
	// Repo is the repository id: the remote repository of a row, the virtual
	// repository of a KindVirtual change.
	Repo int64 `json:"r"`
	// ID is the row id of a KindAdd change.
	ID int64 `json:"i,omitempty"`
	// Prefix is the encoded prefix of a KindAdd or KindVirtual change; use
	// [EncodePrefix] and [Payload.DecodePrefix].
	Prefix string `json:"p,omitempty"`
	Scope  string `json:"s,omitempty"`
	// CreatedUs is the creation time of the row, microseconds since the epoch.
	CreatedUs int64 `json:"t,omitempty"`
}

// EncodePrefix encodes a prefix for a payload.
func EncodePrefix(p string) string { return base64.RawURLEncoding.EncodeToString([]byte(p)) }

// DecodePrefix returns the prefix of the payload.
func (p Payload) DecodePrefix() (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(p.Prefix)
	if err != nil {
		return "", fmt.Errorf("invalidation: prefix of a change payload: %w", err)
	}
	return string(b), nil
}

// ParsePayload decodes a payload of the area. Consumers of the virtual kind
// (the merged-metadata cache of the virtual repositories) use it as well.
func ParsePayload(raw json.RawMessage) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("invalidation: change payload: %w", err)
	}
	return p, nil
}
