package virtual

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
)

// CacheMerged is the registered cache of merged documents (R-ARCH-018). It lives in memory of the
// replica only: a merged document is served to clients, so it is never in Valkey (R-REPO-020,
// R-ARCH-030).
const CacheMerged = "merged_metadata"

// mergedKey is the key of a merged document (R-REPO-020, D-7): the virtual repository, the
// canonical path, the variant, the endpoint and the fingerprint of the members that took part for
// the caller with their populate right. Two callers who see different members, or who may fill
// different members, never share an entry (R-AUTHZ-009 rule 4); callers who see the same do.
//
// Members and Package are redundant with Plan: they are in the key so that an eviction can find the
// entries that contain a member or a package without a second index.
type mergedKey struct {
	Virtual  int64
	Path     string
	Variant  string
	Endpoint string
	Plan     [32]byte
	// Members is the ids of the members that took part, in order, each between commas.
	Members string
	// Package is the SHA-256 of the package name.
	Package [32]byte
}

func newMergedKey(p *Plan, endpoint string) mergedKey {
	k := mergedKey{
		Virtual: repoID(p.Virtual), Path: p.Descriptor.Path, Variant: p.Descriptor.Variant,
		Endpoint: endpoint, Plan: p.Fingerprint, Package: sha256.Sum256([]byte(p.Descriptor.Package)),
	}
	var ids strings.Builder
	ids.WriteByte(',')
	for _, m := range p.Members {
		ids.WriteString(strconv.FormatInt(m.ID, 10))
		ids.WriteByte(',')
	}
	k.Members = ids.String()
	return k
}

// has reports whether the entry was built from a member.
func (k mergedKey) has(id int64) bool {
	return strings.Contains(k.Members, ","+strconv.FormatInt(id, 10)+",")
}

// localRev is the rev_seq of one eligible local member at the time of the merge, 0 when the member
// did not hold the package.
type localRev struct {
	ID  int64
	Rev int64
}

// mergedEntry is a merged document with what validates it.
type mergedEntry struct {
	bytes       []byte
	fingerprint [32]byte
	locals      []localRev
	// expires is the earlier of mergedMetadataTtlSeconds after the merge and the earliest expiry of
	// the remote documents it was built from.
	expires time.Time
	// keep is false for a result that must not be stored (a partial merge); see weigh.
	keep bool
	// partial: a member was left out because it could not be asked (R-REPO-020).
	partial bool
	// source and status describe the build that made the entry: the tier that served it for
	// trove_virtual_resolutions_total and the Cache-Status of the response that built it.
	source, status string
}

func registerMerged(r *cache.Registry, o Options) *cache.Cache[mergedKey, *mergedEntry] {
	// The signal of an early eviction is registered by Start; the change areas are listed here so
	// the fill rule of the cache sees their epochs: a result computed while claims, the repository
	// configuration or an invalidation changed is served and not stored.
	return cache.Register(r, CacheMerged, cache.Options[mergedKey, *mergedEntry]{
		MaxBytes:  o.CacheBytes,
		Weigh:     weigh(o.CacheBytes),
		SafetyTTL: o.SafetyTTL,
		Areas:     []string{cluster.AreaCacheInvalidation, cluster.AreaClaims, cluster.AreaRepoConfig},
		Now:       o.Now,
		OnChange:  onMergedChange,
	})
}

// weigh sizes an entry by its bytes. An entry that must not be kept weighs more than the whole
// cache, which the cache treats as "not stored": the fill rule of Load then applies to the entries
// that are kept and a partial merge needs no second code path.
func weigh(limit int64) func(mergedKey, *mergedEntry) int64 {
	return func(k mergedKey, e *mergedEntry) int64 {
		if !e.keep {
			return limit + 1
		}
		return int64(len(e.bytes)) + int64(len(k.Path)+len(k.Variant)+len(k.Endpoint)+len(k.Members)) + 256
	}
}

// onMergedChange handles the change areas. A virtual invalidation (R-REPO-045) drops the entries of
// the virtual repository under its prefix and, because the remote documents it was built from are
// invalidated too, the entries that contain a remote repository under the prefix of its row. Every
// other change (claims, repository configuration, a kind this build does not know) flushes the
// cache: those change who takes part, and an entry is cheap to build again.
func onMergedChange(c *cache.Cache[mergedKey, *mergedEntry], area string, _ int, payload json.RawMessage) {
	if area != cluster.AreaCacheInvalidation {
		c.Flush(cache.ReasonChange)
		return
	}
	p, err := invalidation.ParsePayload(payload)
	if err != nil {
		c.Flush(cache.ReasonChange)
		return
	}
	switch p.Kind {
	case invalidation.KindVirtual, invalidation.KindAdd:
		prefix, err := p.DecodePrefix()
		if err != nil {
			c.Flush(cache.ReasonChange)
			return
		}
		c.InvalidateFunc(func(k mergedKey) bool {
			if !invalidation.Covers(prefix, k.Path) {
				return false
			}
			if p.Kind == invalidation.KindVirtual {
				return k.Virtual == p.Repo
			}
			return k.has(p.Repo)
		})
	case invalidation.KindDelete:
		// The fold task deleted rows that no longer matter to a merged document.
	default:
		c.Flush(cache.ReasonChange)
	}
}

// PackageChanged is the payload of the signal area metadata_changed (R-REPO-020): the id of the
// repository whose metadata changed and the SHA-256 of the normalized package name. The publisher
// of a local change and the remote engine after a refresh with new content publish it after their
// commit.
func PackageChanged(repoID int64, name string) string {
	sum := sha256.Sum256([]byte(name))
	return strconv.FormatInt(repoID, 10) + ":" + hex.EncodeToString(sum[:])
}

// parsePackageChanged is the inverse of PackageChanged.
func parsePackageChanged(s string) (repoID int64, pkg [32]byte, ok bool) {
	id, hash, found := strings.Cut(s, ":")
	if !found {
		return 0, pkg, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	raw, herr := hex.DecodeString(hash)
	if err != nil || herr != nil || len(raw) != len(pkg) {
		return 0, pkg, false
	}
	copy(pkg[:], raw)
	return n, pkg, true
}

// onMetadataChanged drops the entries that contain the changed member and are of the changed
// package. It only evicts early: the rev_seq check keeps local members exact, and a remote change
// reaches an entry at the latest when it expires. A coarse signal, or a payload this build can not
// read, drops everything.
func (r *Resolver) onMetadataChanged(payloads []string, coarse bool) {
	if r.merged == nil {
		return
	}
	if coarse {
		r.merged.Flush(cache.ReasonChange)
		return
	}
	type target struct {
		repo int64
		pkg  [32]byte
	}
	var targets []target
	for _, p := range payloads {
		id, pkg, ok := parsePackageChanged(p)
		if !ok {
			r.merged.Flush(cache.ReasonChange)
			return
		}
		targets = append(targets, target{id, pkg})
	}
	if len(targets) == 0 {
		return
	}
	r.merged.InvalidateFunc(func(k mergedKey) bool {
		for _, t := range targets {
			if k.Package == t.pkg && k.has(t.repo) {
				return true
			}
		}
		return false
	})
}
