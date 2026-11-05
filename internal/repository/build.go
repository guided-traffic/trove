package repository

import (
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
)

// Build assembles the read model of a repository from its identity, its config version and its stored document, the way the snapshot does
// (patterns compiled, the remote section parsed). It exists for the tests of the packages that consume the model without a
// database, such as the mirror engine's; the readers of production go through the snapshot. A nil catalog is the default
// one. The error is the one of a stored document that does not load; the repository is then returned disabled.
func Build(id, version int64, key string, class Class, format string, config []byte, cat *catalog.Registry) (Repository, error) {
	return toRepository(q.Repository{ID: id, ConfigVersion: version, Key: key, Class: string(class), Format: format, Config: config, Lifecycle: string(Active)}, catalog.Or(cat))
}
