package deb

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/deb/index"
	"github.com/guided-traffic/trove/internal/formats/deb/release"
	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/mirror/discover"
	"github.com/guided-traffic/trove/internal/openpgp"
)

// The build of a virtual repository's indexes (R-DEB-025, R-DEB-026): the members' indexes are merged as streams, in member order, with a
// bounded set of what was kept, into one index per suite, component and architecture, which is signed with the virtual repository's own
// key and published through the port like a local repository's.

// The reasons of a build that merges nothing and keeps the previous generation (R-DEB-025).
const (
	ReasonMemberUnverified  = "member_unverified"
	ReasonMemberExpired     = "member_expired"
	ReasonMemberRollback    = "member_rollback"
	ReasonMemberUnavailable = "member_unavailable"
)

// memberError is the failure of a member whose release may not be merged, or that could not be asked and has no stored copy.
type memberError struct{ reason, member, detail string }

func (e *memberError) Error() string {
	s := e.reason + ": " + e.member
	if e.detail != "" {
		s += " (" + e.detail + ")"
	}
	return s
}

// VirtualMember is a member of a virtual repository as the build walks it, in the order of R-REPO-019 step 5.
type VirtualMember struct {
	ID    int64
	Key   string
	Class string
	// URL is remote.url of a remote member: its upstream identity (Q-DEB-43).
	URL string
	Ref items.RepoRef
}

// VirtualRepo is a virtual Debian repository as a build reads it from the repository model: its names, the settings of its indexes, its
// members in order and a fingerprint of the configuration that decides the merge (the repository's version and its members', and the
// claims).
type VirtualRepo struct {
	Ref                               items.RepoRef
	Project, Key                      string
	Settings                          index.Settings
	Suites, Components, Architectures []string
	Members                           []VirtualMember
	Fingerprint                       string
}

// VirtualRepos looks a virtual repository up and answers which members serve a package name, on stored configuration only: the patterns,
// the pin rules, the claims and the priority members (R-REPO-019 steps 2, 3 and 5), never the caller (Q-DEB-19).
type VirtualRepos interface {
	Virtual(ctx context.Context, id int64) (VirtualRepo, bool, error)
	Allowed(ctx context.Context, id int64, name string) (map[string]bool, error)
}

// MemberIndexes reads a generated item of a local member: the index files of its published generation.
type MemberIndexes interface {
	Open(ctx context.Context, repo items.RepoRef, path string) (rc io.ReadCloser, found bool, err error)
}

// maxIndexRead bounds the decompressed index of a local member read by a merge (R-DEB-034).
const maxIndexRead = 1 << 30

// memberInput is one member's contribution to a suite: a local member's published generation, or a remote member's verified release.
type memberInput struct {
	m     VirtualMember
	local *rows.LocalSuite
	// remote
	rel  rows.Release
	dir  string
	refs map[string]rows.IndexRef
	up   discover.Upstream
}

// virtualInputs is what a build compared and stored with the generation (R-DEB-026).
type virtualInputs struct {
	Fingerprint string                  `json:"fingerprint"`
	Members     map[string]memberRecord `json:"members"`
}

type memberRecord struct {
	// Date is the Date of the release the build merged (Q-DEB-43); Identity the upstream it came from.
	Date       string `json:"date,omitempty"`
	Identity   string `json:"identity,omitempty"`
	Release    string `json:"release,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

func (x *Indexer) snapshotVirtual(ctx context.Context, vr VirtualRepo) (rows.VirtualInputs, error) {
	var locals []int64
	for _, m := range vr.Members {
		if m.Class == "local" {
			locals = append(locals, m.ID)
		}
	}
	var in rows.VirtualInputs
	err := x.Store.virtualStore.o.DB.InTx(ctx, func(tx meta.Tx) (err error) {
		in, err = x.Store.VirtualInputs(ctx, tx, vr.Ref.ID, locals)
		return err
	})
	return in, err
}

// buildVirtual builds the suites of a virtual repository: the suites of deb.suites and those of its local members (R-DEB-025).
func (x *Indexer) buildVirtual(ctx context.Context, vr VirtualRepo, args IndexJobArgs) error {
	in, err := x.snapshotVirtual(ctx, vr)
	if err != nil {
		return err
	}
	wanted := slices.Clone(vr.Suites)
	for _, m := range vr.Members {
		for _, ls := range in.Locals[m.ID] {
			wanted = append(wanted, ls.Suite)
		}
	}
	slices.Sort(wanted)
	wanted = slices.Compact(wanted)
	have := map[string]bool{}
	for _, s := range in.Suites {
		have[s.Suite.Suite] = true
	}
	var missing []string
	for _, s := range wanted {
		if !have[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		if err := x.Store.EnsureSuites(ctx, vr.Ref.ID, missing); err != nil {
			return err
		}
		if in, err = x.snapshotVirtual(ctx, vr); err != nil {
			return err
		}
	}
	keys, err := x.openKeys(ctx, vr.Ref.ID, in.Now)
	if err != nil {
		return err
	}
	byName := map[string]rows.VirtualSuite{}
	for _, s := range in.Suites {
		byName[s.Suite.Suite] = s
	}
	if len(keys) == 0 {
		for _, s := range wanted {
			vs := byName[s]
			if ferr := x.Store.FailBuild(ctx, rows.BuildFailure{RepoID: vr.Ref.ID, Suite: s, ReadSeq: vs.Suite.BuiltSeq + 1, Reason: ReasonNoSigningKey}); ferr != nil {
				return ferr
			}
		}
		x.log().Warn("the indexes of a virtual repository were not built: it has no primary signing key", "repository", vr.Project+"/"+vr.Key)
		return nil
	}
	var first error
	for _, s := range wanted {
		vs := byName[s]
		started := time.Now()
		outcome, err := x.virtualSuite(ctx, vr, in, vs, keys, args.Full)
		x.built("virtual", outcome, started)
		if err == nil {
			continue
		}
		x.log().Error("the build of a suite of a virtual repository failed", "repository", vr.Project+"/"+vr.Key, "suite", s, "error", err)
		if ferr := x.Store.FailBuild(ctx, rows.BuildFailure{RepoID: vr.Ref.ID, Suite: s, ReadSeq: vs.Suite.BuiltSeq + 1, Reason: reasonOf(err)}); ferr != nil {
			err = errors.Join(err, ferr)
		}
		var me *memberError
		if first == nil && !errors.As(err, &me) {
			// A member that may not be merged is a state the next change of the member fixes; any other failure is retried.
			first = err
		}
	}
	// A suite no longer served (a suite taken out of deb.suites whose local members lost it) goes.
	pub := x.Publisher()
	for _, vs := range in.Suites {
		if slices.Contains(wanted, vs.Suite.Suite) || pub == nil {
			continue
		}
		if err := x.dropSuite(ctx, pub, IndexRepo{Ref: vr.Ref, Project: vr.Project, Key: vr.Key}, vs.Suite, generationsOf(vs.Generations)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// virtualSuite merges, signs and publishes one suite of a virtual repository, or records that its inputs did not change.
func (x *Indexer) virtualSuite(ctx context.Context, vr VirtualRepo, in rows.VirtualInputs, vs rows.VirtualSuite, keys []*openpgp.Key, full bool) (string, error) {
	suite := vs.Suite.Suite
	var prev virtualInputs
	_ = json.Unmarshal(vs.Suite.Inputs, &prev) //nolint:errcheck // inputs we wrote; an unreadable record is no record
	pub := x.Publisher()
	if pub == nil {
		return OutcomeFailed, errors.New("deb: the publication port is not available on this replica")
	}
	curr := virtualInputs{Members: map[string]memberRecord{}}
	var parts []memberInput
	var validUntil *time.Time
	for _, m := range vr.Members {
		part, rec, until, err := x.memberInput(ctx, vr, in, m, suite, prev)
		if err != nil {
			return OutcomeFailed, err
		}
		if part == nil {
			continue
		}
		parts = append(parts, *part)
		curr.Members[m.Key] = rec
		if until != nil && (validUntil == nil || until.Before(*validUntil)) {
			validUntil = until
		}
	}
	archs := slices.DeleteFunc(slices.Clone(vr.Architectures), func(a string) bool { return a == index.All })
	curr.Fingerprint = fingerprintOf(vr, suite, curr.Members, validUntil, fingerprints(keys))
	raw, err := json.Marshal(curr)
	if err != nil {
		return OutcomeFailed, err
	}
	if !full && vs.Suite.Generation > 0 && prev.Fingerprint == curr.Fingerprint {
		err := x.Store.virtualStore.o.DB.InTx(ctx, func(tx meta.Tx) error {
			return x.Store.RecordCheck(ctx, tx, vr.Ref.ID, suite, raw, time.Now())
		})
		return OutcomeUnchanged, err
	}

	var listed, files []index.File
	cache := map[string]map[string]bool{}
	var keepErr error
	for _, comp := range vr.Components {
		for _, arch := range append(slices.Clone(archs), index.All) {
			base := comp + "/binary-" + arch + "/Packages"
			w, err := index.NewPackagesWriter(vr.Settings.XZ)
			if err != nil {
				return OutcomeFailed, err
			}
			seen := index.NewSeen(0)
			// The index of an architecture lists the architecture all packages too (Q-DEB-110): a member's own index of the architecture
			// first, then its binary-all, in which an upstream may keep them alone. The set of kept packages drops what both list.
			sources := []string{base}
			if arch != index.All {
				sources = append(sources, comp+"/binary-"+index.All+"/Packages")
			}
			for _, part := range parts {
				for _, source := range sources {
					r, ok, err := x.openMemberIndex(ctx, part, suite, source)
					if err != nil {
						return OutcomeFailed, err
					}
					if !ok {
						continue
					}
					_, err = index.MergeMember(w, seen, part.m.Key, r, func(name string) bool {
						allowed, ok := cache[name]
						if !ok {
							var aerr error
							if allowed, aerr = x.Virtual.Allowed(ctx, vr.Ref.ID, name); aerr != nil {
								keepErr = aerr
							}
							cache[name] = allowed
						}
						return allowed[part.m.Key]
					})
					_ = r.Close()
					if err == nil {
						err = keepErr
					}
					if err != nil {
						return OutcomeFailed, err
					}
				}
			}
			l, fs, err := w.Close(base)
			if err != nil {
				return OutcomeFailed, err
			}
			listed = append(listed, l)
			files = append(files, fs...)
		}
	}
	date, err := x.chooseDate(ctx, in.Now, vs.Suite.ReleaseDate, earliestOf(keys))
	if err != nil {
		return OutcomeFailed, err
	}
	set := vr.Settings
	set.Description = vr.Project + "/" + vr.Key + " " + suite
	built, err := index.Assemble(suite, archs, vr.Components, listed, files, set, date, validUntil)
	if err != nil {
		return OutcomeFailed, err
	}
	var current map[string]string
	for _, g := range vs.Generations {
		if g.SupersededAt == nil {
			current = g.Files
		}
	}
	repo := IndexRepo{Ref: vr.Ref, Project: vr.Project, Key: vr.Key, Settings: vr.Settings}
	outcome, err := x.publishBuilt(ctx, pub, repo, vs.Suite, vs.Generations, in.Now, built, date, keys, true, raw, nil, current)
	if err != nil || outcome != OutcomePublished {
		return outcome, err
	}
	x.enqueueParses(ctx, parts, base(archs, vr.Components))
	return outcome, nil
}

// base lists the index paths of a suite a member's rows are parsed for.
func base(archs, comps []string) []string {
	var out []string
	for _, c := range comps {
		for _, a := range append(slices.Clone(archs), index.All) {
			out = append(out, c+"/binary-"+a+"/Packages")
		}
	}
	return out
}

// enqueueParses asks for the parse of every index of a remote member that the merge read, so that the shadowing check has rows in the
// virtual repository's scope (R-DEB-023, R-DEB-026). The parse job skips an index whose rows are current.
func (x *Indexer) enqueueParses(ctx context.Context, parts []memberInput, paths []string) {
	for _, part := range parts {
		if part.local != nil {
			continue
		}
		for _, p := range paths {
			if _, ok := part.refs[p]; !ok {
				continue
			}
			if err := x.Store.virtualStore.o.DB.InTx(ctx, func(tx meta.Tx) error {
				return x.Store.EnqueueParse(ctx, tx, part.m.ID, part.dir, p)
			}); err != nil {
				x.log().Warn("the parse of a remote member's index could not be enqueued", "member", part.m.Key, "index", p, "error", err)
			}
		}
	}
}

// memberInput finds what one member contributes to a suite: nothing (nil) when a local member has no such suite or a remote's upstream
// does not, or the member's published generation or verified release with the record of it. A release that may not be merged is a
// memberError (R-DEB-025).
func (x *Indexer) memberInput(ctx context.Context, vr VirtualRepo, in rows.VirtualInputs, m VirtualMember, suite string, prev virtualInputs) (*memberInput, memberRecord, *time.Time, error) {
	switch m.Class {
	case "local":
		for i, ls := range in.Locals[m.ID] {
			if ls.Suite == suite {
				return &memberInput{m: m, local: &in.Locals[m.ID][i]}, memberRecord{Generation: ls.Generation}, nil, nil
			}
		}
		return nil, memberRecord{}, nil, nil
	case "remote":
		return x.remoteMember(ctx, vr, m, suite, prev, in.Now)
	}
	return nil, memberRecord{}, nil, nil
}

func (x *Indexer) remoteMember(ctx context.Context, vr VirtualRepo, m VirtualMember, suite string, prev virtualInputs, now time.Time) (*memberInput, memberRecord, *time.Time, error) {
	none := memberRecord{}
	if x.Remote == nil {
		return nil, none, nil, &memberError{reason: ReasonMemberUnavailable, member: m.Key, detail: "this replica builds no remote members"}
	}
	info, ok, err := x.Remote.repo(ctx, m.ID)
	if err != nil {
		return nil, none, nil, err
	}
	if !ok || info.Class != "remote" {
		return nil, none, nil, nil
	}
	if info.Keys == "" {
		return nil, none, nil, &memberError{reason: ReasonMemberUnverified, member: m.Key, detail: "no upstream keys"}
	}
	up, err := x.Remote.Background.Upstream(ctx, m.ID, "virtual-"+vr.Key)
	if err != nil {
		return nil, none, nil, &memberError{reason: ReasonMemberUnavailable, member: m.Key, detail: err.Error()}
	}
	dir := "dists/" + suite
	// The engine decides whether its stored copy is fresh and revalidates it when it is not; a copy of the upstream that the keys do not
	// verify is not stored, and the build says so instead of merging the older copy it still holds (R-DEB-025).
	ferr := x.Remote.fetchRelease(ctx, up, info, m.ID, dir)
	if e, isErr := errs.As(ferr); isErr && (e.Code == errs.CodeDebUpstreamSignatureInvalid || e.Code == errs.CodeDebReleaseInvalid) {
		if strings.Contains(ferr.Error(), "was valid until") {
			return nil, none, nil, &memberError{reason: ReasonMemberExpired, member: m.Key, detail: e.Code}
		}
		return nil, none, nil, &memberError{reason: ReasonMemberUnverified, member: m.Key, detail: e.Code}
	}
	rel, has, err := x.Store.Release(ctx, m.ID, dir)
	if err != nil {
		return nil, none, nil, err
	}
	switch {
	case has && rel.VerifiedBy != "":
		// A member that failed transiently (ferr) contributes its stored copy.
	case ferr != nil:
		return nil, none, nil, &memberError{reason: ReasonMemberUnavailable, member: m.Key, detail: ferr.Error()}
	case has:
		return nil, none, nil, &memberError{reason: ReasonMemberUnverified, member: m.Key}
	default:
		return nil, none, nil, nil // the upstream has no such suite: the member contributes nothing and is no failure
	}
	// The stored copy was verified when the engine stored it (VerifiedBy); the text is read from the same copy to take its Valid-Until.
	parsed, err := x.Remote.releaseText(ctx, up, dir, now)
	if err != nil {
		return nil, none, nil, &memberError{reason: ReasonMemberUnverified, member: m.Key, detail: "the stored release does not read"}
	}
	rec, until, err := admitRelease(m, rel, parsed.ValidUntil, prev, now)
	if err != nil {
		return nil, none, nil, err
	}
	files, err := x.Store.Files(ctx, m.ID, dir)
	if err != nil {
		return nil, none, nil, err
	}
	refs := map[string]rows.IndexRef{}
	for _, r := range rows.PreferredIndexes(files, false) {
		refs[r.Path] = r
	}
	return &memberInput{m: m, rel: rel, dir: dir, refs: refs, up: up}, rec, until, nil
}

// admitRelease applies the rules of R-DEB-025 to the verified release a remote member holds: an expired one is not merged, nor one
// older than the Date the last build recorded for the same upstream (Q-DEB-43). It returns the record of the release for the next build
// and its Valid-Until, if it has one. A record of another upstream (the member's url changed) is not compared, and one of a member that
// left is not carried over, because the next record holds the members of that build only.
func admitRelease(m VirtualMember, rel rows.Release, validUntil time.Time, prev virtualInputs, now time.Time) (memberRecord, *time.Time, error) {
	var until *time.Time
	if !validUntil.IsZero() {
		u := validUntil
		if u.Before(now) {
			return memberRecord{}, nil, &memberError{reason: ReasonMemberExpired, member: m.Key}
		}
		until = &u
	}
	rec := memberRecord{Identity: m.URL, Release: hex.EncodeToString(rel.SHA256[:])}
	if rel.ReleaseDate != nil {
		rec.Date = rel.ReleaseDate.UTC().Format(time.RFC3339)
		if old, ok := prev.Members[m.Key]; ok && old.Identity == m.URL && old.Date != "" {
			if recorded, perr := time.Parse(time.RFC3339, old.Date); perr == nil && rel.ReleaseDate.Before(recorded) {
				return memberRecord{}, nil, &memberError{reason: ReasonMemberRollback, member: m.Key}
			}
		}
	}
	return rec, until, nil
}

// openMemberIndex opens a member's index for a path relative to dists/<suite>/ ("main/binary-amd64/Packages") as a plain stream; ok is
// false when the member has none, which is no failure.
func (x *Indexer) openMemberIndex(ctx context.Context, part memberInput, suite, base string) (io.ReadCloser, bool, error) {
	if part.local != nil {
		if _, ok := part.local.Files[base+".gz"]; !ok {
			return nil, false, nil
		}
		rc, found, err := x.Local.Open(ctx, part.m.Ref, "dists/"+suite+"/"+base+".gz")
		if err != nil || !found {
			return nil, false, err
		}
		zr, err := gzip.NewReader(rc)
		if err != nil {
			_ = rc.Close()
			return nil, false, fmt.Errorf("deb: the index of member %s is not a gzip stream: %w", part.m.Key, err)
		}
		return struct {
			io.Reader
			io.Closer
		}{untrusted.Limit(zr, maxIndexRead), rc}, true, nil
	}
	ref, ok := part.refs[base]
	if !ok {
		return nil, false, nil
	}
	body, err := x.Remote.fetchIndex(ctx, part.up, part.rel, part.dir, ref)
	if err != nil {
		return nil, false, &memberError{reason: ReasonMemberUnavailable, member: part.m.Key, detail: err.Error()}
	}
	if sha256.Sum256(body) != ref.SHA256 {
		return nil, false, &memberError{reason: ReasonMemberUnavailable, member: part.m.Key, detail: "the index is not the file its release names"}
	}
	r, err := decompress(ref.File, body)
	if err != nil {
		return nil, false, &memberError{reason: ReasonMemberUnavailable, member: part.m.Key, detail: err.Error()}
	}
	return io.NopCloser(r), true, nil
}

// fingerprintOf is the digest of what decides the merge of a suite: the configuration, the settings of its indexes, and for each member
// the generation or the release it merges (R-DEB-026).
func fingerprintOf(vr VirtualRepo, suite string, members map[string]memberRecord, until *time.Time, signers []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%v\n", vr.Fingerprint, suite, strings.Join(vr.Components, ","), strings.Join(vr.Architectures, ","), vr.Settings)
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		r := members[k]
		fmt.Fprintf(h, "%s|%s|%s|%d\n", k, r.Identity, r.Release, r.Generation)
	}
	if until != nil {
		fmt.Fprintf(h, "until %d\n", until.Unix())
	}
	// A change of the keys re-signs: the signers are an input like the members (R-DEB-017).
	fmt.Fprintf(h, "signers %s\n", strings.Join(signers, ","))
	return hex.EncodeToString(h.Sum(nil))
}

// triggerVirtuals enqueues the build of every virtual Debian repository that lists the local repository, after its publication, with the
// delay that keeps deb.virtual_index_interval since the virtual repository's last build (R-DEB-026). A failure is logged: the task
// deb-index-refresh enqueues what a lost trigger missed.
func (x *Indexer) triggerVirtuals(ctx context.Context, repo IndexRepo) {
	virtuals, err := x.Store.VirtualsOfMember(ctx, repo.Ref.ID)
	if err != nil || len(virtuals) == 0 {
		if err != nil {
			x.log().Warn("the virtual repositories of a member could not be read", "repository", repo.Project+"/"+repo.Key, "error", err)
		}
		return
	}
	for _, id := range virtuals {
		var delay time.Duration
		if last, err := x.Store.LastBuild(ctx, id); err == nil && !last.IsZero() && x.VirtualInterval > 0 {
			delay = max(0, x.VirtualInterval-time.Since(last))
		}
		if err := x.Store.virtualStore.o.DB.InTx(ctx, func(tx meta.Tx) error { return x.Store.EnqueueBuild(ctx, tx, id, delay) }); err != nil {
			x.log().Warn("the build of a virtual repository could not be enqueued", "virtual", id, "error", err)
		}
	}
}

// releaseText reads the release the engine holds for the directory: the InRelease, else the Release.
func (j *RemoteJobs) releaseText(ctx context.Context, up discover.Upstream, dir string, now time.Time) (*release.Release, error) {
	doc, found, err := up.Document(ctx, join(dir, rows.DocInRelease), "")
	if err != nil {
		return nil, err
	}
	if found {
		r, _, err := release.Signed(doc, nil, now)
		return r, err
	}
	doc, found, err = up.Document(ctx, join(dir, rows.DocRelease), "")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("no release document")
	}
	return release.Parse(doc)
}
