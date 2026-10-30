// Package virtualtest holds the fakes and fixtures for testing virtual resolution without a
// database: repositories with members, a store of local rows that behaves like the package rows, a
// test format whose documents are lists of versions, a programmable remote engine, a router, and a
// real decision point built from a handful of grants. internal/virtual, internal/formathost and the
// integration area test/integration/virtual use it.
package virtualtest

import (
	"strconv"

	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// Format is the ID of the test format.
const Format = "pkgfmt"

type exactNames struct{}

func (exactNames) Normalize(name string) (string, bool) { return name, name != "" }

// Catalog registers the test format: package names, claims, the three classes.
func Catalog() *catalog.Registry {
	r := catalog.NewRegistry()
	r.Register(catalog.Entry{Facts: catalog.Facts{
		ID: Format, Classes: []catalog.Class{catalog.Local, catalog.Remote, catalog.Virtual}, Layout: Format,
		ProtocolBasePath: "/api/" + Format + "/{key}/", PatternSubject: catalog.SubjectName,
		PackageNames: true, PriorityMembers: true, PinsAndClaims: true,
	}, Names: exactNames{}})
	return r
}

// LocalRepo returns a local repository of the test format.
func LocalRepo(id int64, key string) repository.Repository { return repo(id, key, repository.Local) }

// RemoteRepo returns a remote repository of the test format.
func RemoteRepo(id int64, key string) repository.Repository { return repo(id, key, repository.Remote) }

func repo(id int64, key string, class repository.Class) repository.Repository {
	return repository.Repository{
		ID: strconv.FormatInt(id, 10), Key: key, Class: class, Format: Format,
		HideExistence: "inherit", Lifecycle: repository.Active,
	}
}

// VirtualRepo returns a virtual repository of the test format over the members, with the deploy target
// when target is non-nil and the given merged-metadata period.
func VirtualRepo(id int64, key string, ttlSeconds int, target *repository.Repository, members ...repository.Repository) repository.Repository {
	return repository.Assemble(repo(id, key, repository.Virtual), members, target, ttlSeconds)
}
