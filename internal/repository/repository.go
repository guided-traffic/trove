package repository

import (
	"context"
	"time"
)

// Class is the class of a repository (R-REPO-001). It is fixed at creation.
type Class string

// The classes.
const (
	Local   Class = "local"
	Remote  Class = "remote"
	Virtual Class = "virtual"
)

// FormatGeneric is the format of the generic local repository that M1 proves
// raw transfer on (Q-ROAD-1). Other formats arrive with M2.
const FormatGeneric = "generic"

// KeyPattern is the pattern of a repository key (R-REPO-002). A key also must
// not contain ".." or end in "." and must not be a reserved name; a violation
// answers 422 invalid_key or reserved_name.
const KeyPattern = `^[a-z0-9][a-z0-9._-]{1,63}$`

// ReservedKeys are the literal sub-paths of /api/v1/repositories that a key
// cannot take (R-REPO-002).
func ReservedKeys() []string {
	return []string{"batch-create", "batch-update", "batch-get", "batch-delete", "quick-setup"}
}

// Lifecycle is the state of a repository row (R-REPO-013).
type Lifecycle string

// The lifecycle states.
const (
	Active   Lifecycle = "active"
	Deleting Lifecycle = "deleting"
)

// Repository is the read model other packages use. Config is the document of
// R-REPO-004 as stored, without secrets; the repository owner decodes it.
type Repository struct {
	ID     string
	Key    string
	Class  Class
	Format string
	// Disabled repositories take no part in resolution, download or deploy.
	Disabled bool
	// RequireAuthentication challenges anonymous requests even where anonymous
	// read is granted.
	RequireAuthentication bool
	// HideExistence is "inherit", "true" or "false" (R-AUTHZ-017).
	HideExistence string
	Lifecycle     Lifecycle
	// Version is config_version, the ETag source (R-REPO-011).
	Version   int64
	Config    []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Reader is the read interface other packages use. It answers from the
// replica's snapshot, which the change area repo_config keeps current.
type Reader interface {
	// ByKey returns the repository, or an *errs.Error of kind NotFound with code
	// repository_not_found. It does not apply authorization: callers decide
	// first (R-AUTHZ-016: authorization runs before the existence lookup).
	ByKey(ctx context.Context, key string) (Repository, error)
	// List returns every repository, sorted by key. Callers filter it through
	// authz; a listing contains only what the caller may read (R-AUTHZ-016).
	List(ctx context.Context) ([]Repository, error)
}
