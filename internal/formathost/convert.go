package formathost

import (
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/repository"
)

// repoRef converts a repository read model.
func repoRef(r repository.Repository) formats.RepoRef { return refOf(items.RepoOf(r)) }

// refOf converts the item store's repository reference.
func refOf(r items.RepoRef) formats.RepoRef {
	return formats.RepoRef{ID: r.ID, Key: r.Key, Class: formats.Class(r.Class), Format: formats.ID(r.Format)}
}

// itemsRepo converts back to the item store's reference.
func itemsRepo(r formats.RepoRef) items.RepoRef {
	return items.RepoRef{ID: r.ID, Key: r.Key, Class: string(r.Class), Format: string(r.Format)}
}

func itemRef(r items.Ref) formats.ItemRef {
	return formats.ItemRef{ID: r.ID, Repo: refOf(r.Repo), Path: r.Path}
}

func digestsOf(d blob.Digests) formats.Digests {
	return formats.Digests{
		SHA256: append([]byte(nil), d.SHA256[:]...), SHA512: append([]byte(nil), d.SHA512[:]...),
		SHA1: append([]byte(nil), d.SHA1[:]...), MD5: append([]byte(nil), d.MD5[:]...),
	}
}

// itemOf converts an item row; the row itself rides in Handle so that Serve does
// not read it again.
func itemOf(repo formats.RepoRef, it items.Item) formats.Item {
	return formats.Item{
		Ref:    formats.ItemRef{ID: it.ID, Repo: repo, Path: it.Path},
		Folder: it.Kind == items.KindFolder, Size: it.Size, MediaType: it.MediaType, Modified: it.ModifiedAt,
		Digests: formats.Digests{
			SHA256: append([]byte(nil), it.Digest[:]...), SHA512: it.SHA512, SHA1: it.SHA1, MD5: it.MD5,
		},
		Handle: handle{it: it},
	}
}
