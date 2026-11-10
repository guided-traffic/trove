package importer

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// DefaultMaxBytes is the largest file the importer takes from a source when Options.MaxBytes is not set.
const DefaultMaxBytes = 512 << 20

// Options is one import (R-IMP-001).
type Options struct {
	Format Format
	Source *SourceClient
	Target *TargetClient
	// Names are the packages to copy. Search, when set, adds the names the source's search finds for it (R-IMP-003).
	Names  []string
	Search *string
	// Concurrency is the number of packages in flight, 2 by default. The versions of one package go one after the other.
	Concurrency int
	// Dir is where files wait between the download and the publish; the system's temporary directory when empty.
	Dir string
	// MaxBytes is the largest file taken from the source, DefaultMaxBytes when not set.
	MaxBytes int64
	// DryRun reads both sides and reports what would be copied; it downloads nothing and writes nothing.
	DryRun bool
	// Journal records the packages that are complete, so that a run that is started again does not read them again.
	Journal *Journal
	// Progress receives one line per package, names and counts only.
	Progress io.Writer
}

// VersionResult is the outcome of one version.
type VersionResult struct {
	Version string `json:"version"`
	Outcome string `json:"outcome"`
	Code    string `json:"code,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// PackageResult is the outcome of one package.
type PackageResult struct {
	Name     string          `json:"name"`
	Versions []VersionResult `json:"versions,omitempty"`
	Error    string          `json:"error,omitempty"`
	Code     string          `json:"code,omitempty"`
	NotKept  []string        `json:"notKept,omitempty"`
	Notes    []string        `json:"notes,omitempty"`
	// Resumed is true for a package the state file held as complete: it was not read again.
	Resumed bool `json:"resumed,omitempty"`
}

// Count returns the number of versions with an outcome.
func (p PackageResult) Count(outcome string) int {
	n := 0
	for _, v := range p.Versions {
		if v.Outcome == outcome {
			n++
		}
	}
	return n
}

// Complete reports whether every version of the package is in the target: imported or identical.
func (p PackageResult) Complete() bool {
	if p.Error != "" {
		return false
	}
	for _, v := range p.Versions {
		if v.Outcome != OutcomeImported && v.Outcome != OutcomeSkipped {
			return false
		}
	}
	return true
}

// Report is what an import did, per package (R-IMP-006).
type Report struct {
	Format   string          `json:"format"`
	Source   string          `json:"source"`
	Target   string          `json:"target"`
	DryRun   bool            `json:"dryRun,omitempty"`
	Packages []PackageResult `json:"packages"`
	// Aborted says why the run stopped before it had read every package.
	Aborted string `json:"aborted,omitempty"`
}

// Totals counts the versions by outcome and the packages that need a look.
func (r *Report) Totals() (versions map[string]int, packagesWithProblems int) {
	versions = map[string]int{}
	for _, p := range r.Packages {
		for _, v := range p.Versions {
			versions[v.Outcome]++
		}
		if !p.Complete() && (!r.DryRun || p.Error != "" || !onlyWould(p)) {
			packagesWithProblems++
		}
	}
	return versions, packagesWithProblems
}

func onlyWould(p PackageResult) bool {
	for _, v := range p.Versions {
		if v.Outcome != OutcomeWouldAdd && v.Outcome != OutcomeSkipped {
			return false
		}
	}
	return true
}

// OK reports whether the run left nothing to look at.
func (r *Report) OK() bool {
	_, n := r.Totals()
	return r.Aborted == "" && n == 0
}

type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

// Run copies the packages (R-IMP-001). It returns the report also when a package failed; the error is for a run that could not
// start or was cut short (its report holds what was done until then).
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Format == nil || o.Source == nil || o.Target == nil {
		return nil, errors.New("importer: format, source and target are required")
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 2
	}
	rep := &Report{Format: o.Format.ID(), Source: redact(o.Source.Origin.String()), Target: redact(o.Target.Base.String()), DryRun: o.DryRun}
	if err := o.Format.Preflight(ctx, o.Target); err != nil {
		return rep, fmt.Errorf("the target is not usable: %w", err)
	}
	names, err := discover(ctx, o)
	if err != nil {
		return rep, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	r := &run{o: o, mu: &sync.Mutex{}}
	results := make([]PackageResult, len(names))
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(o.Concurrency, max(len(names), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(names) {
					return
				}
				res := r.pkg(ctx, names[i])
				var fe *fatalError
				if errors.As(res.fatal, &fe) {
					cancel(fe)
				}
				results[i] = res.PackageResult
			}
		}()
	}
	wg.Wait()
	for i, res := range results {
		if res.Name == "" {
			// Not reached: the run was cut short.
			results[i] = PackageResult{Name: names[i], Error: "not read: the run was stopped", Code: CodeSourceError}
		}
	}
	rep.Packages = results
	if cause := context.Cause(ctx); cause != nil {
		var fe *fatalError
		if errors.As(cause, &fe) {
			rep.Aborted = fe.msg
		} else {
			rep.Aborted = cause.Error()
		}
		return rep, cause
	}
	return rep, nil
}

// discover builds the sorted, deduplicated list of names to copy.
func discover(ctx context.Context, o Options) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, n := range o.Names {
		add(n)
	}
	if o.Search != nil {
		found, err := o.Format.Search(ctx, o.Source, *o.Search)
		if err != nil {
			return nil, fmt.Errorf("the search of the source failed: %w", err)
		}
		for _, n := range found {
			add(n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("there is nothing to import: give package names or a search")
	}
	return names, nil
}

type run struct {
	o  Options
	mu *sync.Mutex
}

type pkgResult struct {
	PackageResult
	fatal error
}

func (r *run) progress(p PackageResult) {
	if r.o.Progress == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = fmt.Fprintf(r.o.Progress, "%s: %d imported, %d skipped, %d conflicts, %d failed%s\n", p.Name,
		p.Count(OutcomeImported), p.Count(OutcomeSkipped), p.Count(OutcomeConflict), p.Count(OutcomeFailed), errSuffix(p))
}

func errSuffix(p PackageResult) string {
	if p.Error != "" {
		return " (" + p.Code + ")"
	}
	return ""
}

func (r *run) pkg(ctx context.Context, name string) pkgResult {
	f := r.o.Format
	if !f.ValidName(name) {
		return pkgResult{PackageResult: PackageResult{Name: name, Error: "the name is not one the importer puts into an address", Code: CodeInvalidName}}
	}
	if done, ok := r.o.Journal.Done(name); ok {
		done.Resumed = true
		r.progress(done)
		return pkgResult{PackageResult: done}
	}
	res := PackageResult{Name: name}
	plan, err := f.Plan(ctx, r.o.Source, name)
	if err != nil {
		res.Error, res.Code = "the source could not be read: "+err.Error(), CodeSourceError
		return pkgResult{PackageResult: res}
	}
	if len(plan.Versions) == 0 {
		res.Error, res.Code = "the source holds no version of the package", CodeSourceError
		return pkgResult{PackageResult: res}
	}
	res.NotKept = plan.NotKept
	held, tags, err := f.Held(ctx, r.o.Target, name)
	if held == nil {
		held = map[string]Held{}
	}
	if err != nil {
		res.Error, res.Code = "the target could not be read: "+err.Error(), CodeTargetRefused
		return pkgResult{PackageResult: res, fatal: fatalOf(err)}
	}
	var fatal error
	for _, v := range plan.Versions {
		if ctx.Err() != nil {
			break
		}
		have, stored := held[v.Version]
		vr, h, err := r.version(ctx, plan, v, have, stored)
		res.Versions = append(res.Versions, vr)
		if vr.Outcome == OutcomeImported || vr.Outcome == OutcomeSkipped {
			held[v.Version] = h
		}
		if fatal = fatalOf(err); fatal != nil {
			break
		}
	}
	if fatal == nil && ctx.Err() == nil && !r.o.DryRun && (res.Count(OutcomeImported)+res.Count(OutcomeSkipped)) > 0 {
		notes, err := f.Finish(ctx, r.o.Target, plan, held, tags)
		res.Notes = notes
		if err != nil {
			res.Error, res.Code = "state of the package could not be set: "+err.Error(), CodeStateNotSet
			fatal = fatalOf(err)
		}
	}
	if ctx.Err() != nil {
		// Cut short: the package is not complete, whatever was done stays for the next run.
		if res.Error == "" {
			res.Error, res.Code = "the run was stopped before the package was done", CodeSourceError
		}
		return pkgResult{PackageResult: res, fatal: fatal}
	}
	if res.Complete() && !r.o.DryRun {
		if err := r.o.Journal.Record(res); err != nil {
			return pkgResult{PackageResult: res, fatal: &fatalError{"the state file could not be written: " + err.Error()}}
		}
	}
	r.progress(res)
	return pkgResult{PackageResult: res, fatal: fatal}
}

// fatalOf turns the refusals that no other package can get around into the reason the run stops: a token that is not
// accepted (401) and a full storage (507).
func fatalOf(err error) error {
	var ref *Refusal
	if !errors.As(err, &ref) {
		return nil
	}
	switch ref.Status {
	case http.StatusUnauthorized:
		return &fatalError{"the target does not accept the token (401); the run was stopped"}
	case http.StatusInsufficientStorage:
		return &fatalError{"the target is full (507 " + ref.Code + "); the run was stopped"}
	}
	return nil
}

// tagFor is the one dist-tag a publish carries (npm): a tag of the source that points at the version, else latest.
func tagFor(p *Plan, version string) string {
	var tags []string
	for t, v := range p.Tags {
		if v == version && t != "latest" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return "latest"
	}
	sort.Strings(tags)
	return tags[0]
}

func matches(want Digest, sums Sums) bool {
	got, ok := sums[want.Alg]
	return ok && subtle.ConstantTimeCompare(got, want.Sum) == 1
}

func short(alg string, sum []byte) string {
	h := hex.EncodeToString(sum)
	if len(h) > 16 {
		h = h[:16]
	}
	return alg + ":" + h
}

// version copies one version. It returns the outcome and what the target holds of the version afterwards.
func (r *run) version(ctx context.Context, p *Plan, v Version, held Held, stored bool) (VersionResult, Held, error) {
	vr := VersionResult{Version: v.Version}
	fail := func(code, detail string) (VersionResult, Held, error) {
		vr.Outcome, vr.Code, vr.Detail = OutcomeFailed, code, detail
		return vr, Held{}, nil
	}
	if v.Want.Alg == "" {
		return fail(CodeNoDigest, "the source announced no digest to verify the file against")
	}
	if !r.o.Source.Allowed(v.URL) {
		return fail(CodeForeignHost, "the file is not on the source's origin or an allowed extra host")
	}
	if stored {
		// The target holds the version. When it holds the digest in the source's algorithm the two are compared without
		// a download (R-IMP-006); otherwise the file is read and verified first.
		if have, ok := held.Digests[v.Want.Alg]; ok {
			if bytes.Equal(have, v.Want.Sum) {
				vr.Outcome = OutcomeSkipped
				return vr, held, nil
			}
			vr.Outcome, vr.Code = OutcomeConflict, "version_exists"
			vr.Detail = "the target holds the version with " + short(v.Want.Alg, have) + ", the source with " + short(v.Want.Alg, v.Want.Sum)
			return vr, held, nil
		}
	}
	if r.o.DryRun {
		if stored {
			vr.Outcome = OutcomeSkipped
			vr.Detail = "the target holds the version; the content was not compared in a dry run"
			return vr, held, nil
		}
		vr.Outcome = OutcomeWouldAdd
		return vr, held, nil
	}
	f, err := r.o.Source.Download(ctx, v.URL, r.o.Dir, r.o.MaxBytes)
	switch {
	case errors.Is(err, ErrTooLarge):
		return fail(CodeTooLarge, fmt.Sprintf("the file is larger than %d bytes", r.o.MaxBytes))
	case errors.Is(err, ErrForeignHost):
		return fail(CodeForeignHost, "the file is not on the source's origin or an allowed extra host")
	case err != nil:
		if ctx.Err() != nil {
			return fail(CodeSourceError, "stopped")
		}
		return fail(CodeSourceError, err.Error())
	}
	defer f.Remove()
	if !matches(v.Want, f.Sums) {
		return fail(CodeDigestMismatch, "the file is not the one the source announced ("+short(v.Want.Alg, v.Want.Sum)+"); it was not sent to the target")
	}
	if stored {
		for _, alg := range []string{"sha512", "sha256", "sha1"} {
			have, ok := held.Digests[alg]
			if !ok {
				continue
			}
			if bytes.Equal(have, f.Sums[alg]) {
				vr.Outcome = OutcomeSkipped
				return vr, held, nil
			}
			vr.Outcome, vr.Code, vr.Detail = OutcomeConflict, "version_exists", "the target holds the version with other content"
			return vr, held, nil
		}
		return fail(CodeTargetRefused, "the target holds the version, but its digest could not be read to compare")
	}
	err = r.o.Format.Publish(ctx, r.o.Target, p, v, f, tagFor(p, v.Version))
	var ref *Refusal
	var item *ItemError
	switch {
	case errors.As(err, &item):
		return fail(item.Code, item.Detail)
	case err == nil:
		vr.Outcome = OutcomeImported
		return vr, Held{Digests: map[string][]byte{"sha512": f.Sums["sha512"], "sha1": f.Sums["sha1"], "sha256": f.Sums["sha256"]}, Listed: true}, nil
	case errors.As(err, &ref):
		vr.Detail = "the target answered " + fmt.Sprint(ref.Status) + codeSuffix(ref)
		switch ref.Status {
		case http.StatusConflict:
			vr.Outcome, vr.Code = OutcomeConflict, ref.Code
			if vr.Code == "" {
				vr.Code = "version_exists"
			}
		case http.StatusForbidden:
			vr.Outcome, vr.Code = OutcomeFailed, CodeForbidden
		default:
			vr.Outcome, vr.Code = OutcomeFailed, CodeTargetRefused
		}
		return vr, Held{}, err
	default:
		if ctx.Err() != nil {
			return fail(CodeTargetRefused, "stopped")
		}
		return fail(CodeTargetRefused, err.Error())
	}
}

func codeSuffix(r *Refusal) string {
	if r.Code == "" {
		return ""
	}
	return " " + r.Code
}
