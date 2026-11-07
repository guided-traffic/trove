package create

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
)

// previousBackup is the newest complete backup of the target, any incarnation (R-BAK-006 step 1).
type previousBackup struct {
	id       format.ID
	manifest *format.Manifest
}

// preflight is the target half of the phase 'starting' (R-BAK-009): the encryption the target applies (R-BAK-026), the marker
// and a possible adoption (R-BAK-002), and the listing that tells the previous backup and whether a predecessor left objects.
func (r *runState) preflight(ctx context.Context) error {
	if err := r.checkEncryption(ctx); err != nil {
		return err
	}
	if err := r.checkMarker(ctx); err != nil {
		return err
	}
	if r.d.Cfg.Target.AllowSameBucket {
		r.warn("backup.target.allow_same_bucket is set: the backup shares the failure modes of the blob bucket (R-BAK-002)")
	}
	if r.opts.ExcludeRemoteCaches || r.d.Cfg.Exclude.RemoteCaches {
		r.warn("remote-repository caches are left out of this backup: content the upstream no longer serves is lost on restore (R-BAK-008)")
	}
	return r.listTarget(ctx)
}

func (r *runState) warn(msg string) {
	r.d.Log.Warn(msg)
	r.res.Warnings = append(r.res.Warnings, msg)
}

// wantSSE is the encryption the target must report for the configured mode.
func wantSSE(mode string) string {
	switch mode {
	case "s3":
		return "AES256"
	case "kms":
		return "aws:kms"
	}
	return ""
}

// checkEncryption writes a probe object and compares the server-side encryption the target reports with backup.target.sse.mode;
// a mismatch is exit 2 (R-BAK-026).
func (r *runState) checkEncryption(ctx context.Context) error {
	sse := r.d.Cfg.Target.SSE
	obj, err := r.d.Target.Probe(ctx, r.probeKey())
	if err != nil {
		return classify(fmt.Errorf("probe the backup target: %w", err))
	}
	r.h.Tick()
	want := wantSSE(sse.Mode)
	if obj.SSE != want {
		return backup.Failf(backup.ExitUsage, backup.CodeEncryptionMismatch,
			"the backup target reports server-side encryption %q but backup.target.sse.mode %s asks for %q", obj.SSE, sse.Mode, want)
	}
	if sse.Mode == "kms" && sse.KMSKeyID != "" && obj.KMSKeyID == "" {
		return backup.Failf(backup.ExitUsage, backup.CodeEncryptionMismatch,
			"the backup target reports no KMS key but backup.target.sse.kms_key_id is %q: backup.target.sse.mode kms needs a store that names the key", sse.KMSKeyID)
	}
	if sse.Mode == "kms" && sse.KMSKeyID != "" && !strings.HasSuffix(obj.KMSKeyID, sse.KMSKeyID) && !strings.HasSuffix(sse.KMSKeyID, obj.KMSKeyID) {
		return backup.Failf(backup.ExitUsage, backup.CodeEncryptionMismatch,
			"the backup target encrypts with key %q but backup.target.sse.kms_key_id is %q", obj.KMSKeyID, sse.KMSKeyID)
	}
	switch sse.Mode {
	case "none":
		r.sse = "none"
		r.warn("backup.target.sse.mode is none: the backup objects are not encrypted at rest on the target (R-BAK-026)")
	default:
		r.sse = want
	}
	r.res.Encryption = r.sse
	return nil
}

func (r *runState) probeKey() string {
	base := "_probe/" + string(r.id)
	if r.layout.Prefix == "" {
		return base
	}
	return r.layout.Prefix + "/" + base
}

// checkMarker reads the target marker, writes it on an empty prefix, and adopts it after a restore when the flag and the
// lineage allow (R-BAK-002). A marker of another installation or incarnation is exit 4.
func (r *runState) checkMarker(ctx context.Context) error {
	key := r.layout.Marker()
	m, found, err := r.readMarker(ctx)
	if err != nil {
		return err
	}
	if !found {
		if err := r.requireEmptyPrefix(ctx); err != nil {
			return err
		}
		nm := format.NewMarker(r.facts.InstallationID, r.facts.Incarnation, r.d.Now())
		b, err := nm.Encode()
		if err != nil {
			return err
		}
		_, err = r.d.Target.Put(ctx, key, b, target.PutOptions{IfAbsent: true, ContentType: "application/json"})
		switch {
		case err == nil:
			r.d.Log.Info("wrote the backup target marker", "installation", r.facts.InstallationID)
			return nil
		case errors.Is(err, target.ErrExists):
			// Another installation wrote it between our read and our write: judge theirs.
			if m, found, err = r.readMarker(ctx); err != nil {
				return err
			}
		case errors.Is(err, target.ErrConditionalUnsupported):
			if _, err = r.d.Target.Put(ctx, key, b, target.PutOptions{ContentType: "application/json"}); err != nil {
				return classify(fmt.Errorf("write the target marker: %w", err))
			}
			return nil
		default:
			return classify(fmt.Errorf("write the target marker: %w", err))
		}
	}
	if !found {
		return errors.New("the target marker vanished after it was written")
	}
	checkErr := m.Check(r.facts.InstallationID, r.facts.Incarnation)
	if checkErr == nil {
		return nil
	}
	if !r.opts.AdoptTarget {
		return backup.Fail(backup.ExitForeignTarget, backup.CodeForeignTarget, checkErr)
	}
	rf := r.facts.RestoredFrom
	if rf == nil || !m.CanAdopt(rf.InstallationID, rf.Incarnation) {
		return backup.Failf(backup.ExitForeignTarget, backup.CodeForeignTarget,
			"--adopt-target refused: the marker names installation %s, incarnation %s, which is not the lineage this installation was restored from; %w",
			m.InstallationID, m.Incarnation, checkErr)
	}
	nm := m.Adopt(r.facts.InstallationID, r.facts.Incarnation, r.d.Now())
	b, err := nm.Encode()
	if err != nil {
		return err
	}
	if _, err := r.d.Target.Put(ctx, key, b, target.PutOptions{ContentType: "application/json"}); err != nil {
		return classify(fmt.Errorf("adopt the target marker: %w", err))
	}
	r.d.Log.Warn("adopted the backup target of the installation this one was restored from", "previous_incarnation", m.Incarnation)
	r.event(ctx, audit.Event{
		Type: audit.EventBackupTargetAdopted, Actor: actor(), Outcome: audit.Success,
		Details: mustJSON(map[string]string{"previousIncarnation": m.Incarnation}),
	})
	return nil
}

// readMarker returns the marker, or found == false for a target without one.
func (r *runState) readMarker(ctx context.Context) (format.Marker, bool, error) {
	rc, _, err := r.d.Target.Get(ctx, r.layout.Marker())
	if errors.Is(err, target.ErrNotFound) {
		return format.Marker{}, false, nil
	}
	if err != nil {
		return format.Marker{}, false, classify(fmt.Errorf("read the target marker: %w", err))
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return format.Marker{}, false, classify(fmt.Errorf("read the target marker: %w", err))
	}
	m, err := format.DecodeMarker(b)
	var unknown *format.UnknownFormatError
	if err != nil && !errors.As(err, &unknown) {
		// Not a marker this lane wrote: another installation's target, or a foreign object at that key.
		err = backup.Fail(backup.ExitForeignTarget, backup.CodeForeignTarget, fmt.Errorf("the target marker %s is not readable: %w", r.layout.Marker(), err))
	}
	return m, err == nil, err
}

// requireEmptyPrefix: the first run on an empty prefix writes the marker. A prefix that holds backups or pool objects without a
// marker was not written by this lane, and a run must not mix into it (exit 4).
func (r *runState) requireEmptyPrefix(ctx context.Context) error {
	errFound := errors.New("found")
	for _, prefix := range []string{r.layout.BackupsPrefix(), r.layout.PoolPrefix()} {
		err := r.d.Target.List(ctx, prefix, func(target.Object) error { return errFound })
		if errors.Is(err, errFound) {
			return backup.Failf(backup.ExitForeignTarget, backup.CodeForeignTarget,
				"the target prefix %q holds objects below %q but no marker %s: it was not written by this installation", r.layout.Prefix, prefix, r.layout.Marker())
		}
		if err != nil {
			return classify(fmt.Errorf("list the backup target: %w", err))
		}
	}
	return nil
}

// listTarget finds the previous backup (the newest complete one) and whether a predecessor left an incomplete directory newer
// than it, which turns on the existence check of the copy (backup.copy.check_existing: auto).
func (r *runState) listTarget(ctx context.Context) error {
	complete := map[format.ID]bool{}
	seen := map[format.ID]bool{}
	err := r.d.Target.List(ctx, r.layout.BackupsPrefix(), func(o target.Object) error {
		id, inner, ok := r.layout.ParseBackupKey(o.Key)
		if !ok {
			return nil
		}
		seen[id] = true
		if inner == format.ManifestName {
			complete[id] = true
		}
		r.h.Tick()
		return nil
	})
	if err != nil {
		return classify(fmt.Errorf("list the backups of the target: %w", err))
	}
	var newest format.ID
	for id := range complete {
		if newest == "" || newest.Less(id) {
			newest = id
		}
	}
	for id := range seen {
		if !complete[id] && id != r.id && (newest == "" || newest.Less(id)) {
			r.checkExisting = true
		}
	}
	switch r.d.Cfg.Copy.CheckExisting {
	case "always":
		r.checkExisting = true
	case "never":
		r.checkExisting = false
	}
	if newest == "" {
		return nil
	}
	rc, _, err := r.d.Target.Get(ctx, r.layout.Manifest(newest))
	if err != nil {
		return classify(fmt.Errorf("read the manifest of %s: %w", newest, err))
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return classify(fmt.Errorf("read the manifest of %s: %w", newest, err))
	}
	m, err := format.DecodeManifest(b)
	if err != nil {
		return err
	}
	r.prev = &previousBackup{id: newest, manifest: m}
	return nil
}
