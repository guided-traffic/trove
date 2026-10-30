package repository

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

// Class is the class of a repository (R-REPO-001). It is fixed at creation. The
// catalog owns the type, so a format and the repository model name one.
type Class = catalog.Class

// The classes.
const (
	Local   = catalog.Local
	Remote  = catalog.Remote
	Virtual = catalog.Virtual
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
	// LastActivityAt is the latest upload or download as the replicas flushed it
	// (R-REPO-012); zero when there was none.
	LastActivityAt time.Time

	// adm is the compiled include and exclude lists of Config. The snapshot and
	// the store fill it; a Repository built by hand admits everything.
	adm *Admission
	// remote is the parsed remote section of a remote repository (Remote).
	remote *RemoteSettings

	// PriorityMember is the priorityMember setting (R-REPO-022).
	PriorityMember bool
	// rid is the numeric id; autoClaim is local.autoClaim with the format's default
	// (R-REPO-023); virt is the routing view of a virtual repository (virtual.go).
	rid       int64
	autoClaim bool
	virt      *virtualState
}

// Admits reports whether a canonical path is admitted by the repository's
// include and exclude patterns (R-REPO-005). It applies to every caller,
// administrators included: the patterns are routing rules. A folder (a path that
// ended in a slash) is admitted when some path below it could be.
func (r Repository) Admits(path string, folder bool) bool {
	if folder {
		return r.adm.AdmitsFolder(path)
	}
	return r.adm.Admits(path)
}

// Restricts reports whether the repository has include or exclude patterns, that is,
// whether Admits can answer false for some name or path. A listing that counts the
// versions of a repository without asking Admits about each is right only for a
// repository that does not.
func (r Repository) Restricts() bool { return r.adm.restricts() }

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
