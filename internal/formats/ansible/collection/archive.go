// Package collection reads a collection archive, the gzip-compressed tar file `ansible-galaxy collection build` writes and
// `ansible-galaxy collection install` downloads (R-ANS-010, R-ANS-031). The archive is untrusted input: it is read as a bounded stream
// through internal/formats/untrusted, never extracted, and accepted only when it is internally consistent: MANIFEST.json names its
// collection and the digest of FILES.json, FILES.json lists every file of the archive with its digest, and nothing is in the archive
// that FILES.json does not list. The client's `collection verify` has something to verify against then.
//
// It is a leaf: it imports the standard library, the YAML module (for one scalar of meta/runtime.yml), internal/errs and
// internal/formats/untrusted.
package collection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/guided-traffic/trove/internal/formats/ansible/version"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The bounds of the three documents the reader holds in memory (R-ANS-031). Everything else in the archive is streamed through a
// digest and kept nowhere.
const (
	MaxManifestBytes = 1 << 20
	MaxFilesBytes    = 16 << 20
	MaxRuntimeBytes  = 64 << 10
	// MaxDependencies bounds the dependencies of a manifest (R-ANS-031).
	MaxDependencies = 256
	// maxRequirement bounds the version requirement of one dependency.
	maxRequirement = 256
)

// The names of the archive's own documents.
const (
	ManifestName = "MANIFEST.json"
	FilesName    = "FILES.json"
	RuntimeName  = "meta/runtime.yml"
)

// The reasons of an [Error], stable strings that the format maps to the codes of R-ANS-007 and to the messages of a failed import task
// (R-ANS-009).
const (
	// ReasonUnreadable: the content is no gzip-compressed tar archive, or holds a structure no reader should be asked to resolve (a
	// link, a device, a repeated name, data after the end).
	ReasonUnreadable = "unreadable_archive"
	// ReasonBound: a bound was crossed (entries, bytes, a document, the compression ratio).
	ReasonBound = "bound_exceeded"
	// ReasonUnsafeName: an entry name is absolute, leaves the archive or holds a character no file name should.
	ReasonUnsafeName = "unsafe_name"
	// ReasonMissingManifest and ReasonMissingFiles: MANIFEST.json or FILES.json is not at the root of the archive.
	ReasonMissingManifest = "missing_manifest"
	ReasonMissingFiles    = "missing_files_json"
	// ReasonManifestInvalid and ReasonFilesInvalid: the document is not JSON of the shape the format defines.
	ReasonManifestInvalid = "manifest_invalid"
	ReasonFilesInvalid    = "files_invalid"
	// ReasonFilesChecksum: the digest of FILES.json is not the one MANIFEST.json holds.
	ReasonFilesChecksum = "files_checksum_mismatch"
	// ReasonMemberChecksum: a file's digest is not the one FILES.json lists; ReasonMemberMissing: a listed file is not in the archive;
	// ReasonUnlistedMember: a file of the archive is not listed.
	ReasonMemberChecksum = "member_checksum_mismatch"
	ReasonMemberMissing  = "member_missing"
	ReasonUnlistedMember = "unlisted_member"
	// ReasonInvalidName and ReasonInvalidVersion: the identity of the manifest breaks the rules of R-ANS-002.
	ReasonInvalidName    = "invalid_name"
	ReasonInvalidVersion = "invalid_version"
	// ReasonInvalidDependencies: a dependency key is no collection name, a requirement is no string, or there are too many.
	ReasonInvalidDependencies = "invalid_dependencies"
)

// Error is the refusal of an archive: a reason of this package and a sentence a person can act on. Bound is true when a bound was
// crossed (the archive may be a collection, only too large); the caller decides between 413 and a failed task.
type Error struct {
	Reason string
	Detail string
	Bound  bool
	cause  error
}

func (e *Error) Error() string { return e.Reason + ": " + e.Detail }

// Unwrap returns the error of the stream layer that raised the refusal, if there was one.
func (e *Error) Unwrap() error { return e.cause }

func refuse(reason, format string, args ...any) *Error {
	return &Error{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// fromStream maps what the bounded reader returned: a bound crossed or a refused structure become an [Error]; an I/O or context error
// is the caller's and passes unchanged.
func fromStream(err error) error {
	switch {
	case untrusted.IsTooLarge(err):
		return &Error{Reason: ReasonBound, Detail: sentence(err), Bound: true, cause: err}
	case untrusted.IsRefused(err):
		return &Error{Reason: ReasonUnreadable, Detail: sentence(err), cause: err}
	}
	return err
}

func sentence(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i >= 0 && i < 40 && !strings.Contains(s[:i], " ") {
		return s[i+2:]
	}
	return s
}

// Archive is what the reader learned of a valid archive.
type Archive struct {
	Namespace, Name, Version string
	// Info is collection_info reduced to the keys of a version document's metadata (authors, description, documentation, homepage,
	// issues, license, license_file, readme, repository, tags), as compact JSON in the order of [InfoKeys]. The dependencies are
	// in Dependencies and not here.
	Info json.RawMessage
	// Dependencies are the collection_info.dependencies: "<ns>.<name>" to a version requirement.
	Dependencies map[string]string
	// RequiresAnsible is the requires_ansible of meta/runtime.yml, "" when the file is absent or was ignored.
	RequiresAnsible string
	// Files is the number of regular files besides MANIFEST.json and FILES.json.
	Files int
	// ReadmeName is the path of the readme inside the archive that collection_info.readme names and the archive holds, "" when there
	// is none.
	ReadmeName string
	// Warnings are the findings that did not refuse the archive (a meta/runtime.yml that was ignored).
	Warnings []string
}

// FileName is the name `ansible-galaxy collection build` gives the archive of this collection version.
func (a *Archive) FileName() string { return FileName(a.Namespace, a.Name, a.Version) }

// InfoKeys are the keys of collection_info that a version document's metadata carries, in the order the reader writes them.
func InfoKeys() []string {
	return []string{"authors", "description", "documentation", "homepage", "issues", "license", "license_file", "readme", "repository", "tags"}
}

// DefaultLimits are the bounds the format reads an archive under: the defaults of R-ARCH-028 with the one entry that is kept in
// memory raised per call (a FILES.json may be 16 MiB, [MaxFilesBytes]). A format may lower the core's defaults and not raise them,
// so the entry count (10,000) and the inflated total (2 GiB) are the core's (Q-ANS-30).
func DefaultLimits() untrusted.Limits { return untrusted.DefaultLimits() }

// ReadArchive reads and validates the collection archive in r in one pass: the stream is read once, every member goes through a digest
// and the two documents are kept, so the archive needs no random access and a fill can hand it the body it is storing. lim are the
// bounds of the call (the core's, tightened by the format); the deadline of lim ends the whole read. The error is an [*Error] for an
// archive that is refused, and the stream's own error for a failure of r or of ctx.
func ReadArchive(ctx context.Context, r io.Reader, lim untrusted.Limits) (*Archive, error) {
	ctx, cancel := lim.Context(ctx)
	defer cancel()
	tr := untrusted.NewTarGz(ctx, r, lim)
	var (
		manifest, files, runtime []byte
		haveRuntime              bool
		members                  = map[string]string{} // file name to the hex digest of its content
	)
	for {
		e, err := tr.Next()
		if err != nil {
			// A refusal can wrap io.EOF as its cause (an empty body), so the refusal is looked at first.
			if ferr := fromStream(err); !errors.Is(ferr, io.EOF) || errors.As(ferr, new(*Error)) {
				return nil, ferr
			}
			break
		}
		name, err := cleanName(e.Name)
		if err != nil {
			return nil, err
		}
		if e.Dir {
			continue
		}
		if name == "" {
			return nil, refuse(ReasonUnsafeName, "an entry has no name")
		}
		switch name {
		case ManifestName:
			manifest, err = tr.ReadEntry(MaxManifestBytes)
		case FilesName:
			files, err = tr.ReadEntry(MaxFilesBytes)
			if err == nil {
				members[name] = digest(files)
			}
		case RuntimeName:
			haveRuntime = true
			runtime, err = tr.ReadEntry(MaxRuntimeBytes)
			if err == nil {
				members[name] = digest(runtime)
			}
		default:
			h := sha256.New()
			if _, err = tr.Stream(h); err == nil {
				members[name] = hex.EncodeToString(h.Sum(nil))
			}
		}
		if err != nil {
			return nil, fromStream(err)
		}
	}
	switch {
	case manifest == nil:
		return nil, refuse(ReasonMissingManifest, "the archive has no %s at its root", ManifestName)
	case files == nil:
		return nil, refuse(ReasonMissingFiles, "the archive has no %s at its root", FilesName)
	}
	a, err := readManifest(manifest, members[FilesName], lim)
	if err != nil {
		return nil, err
	}
	if err := checkFiles(files, members, lim); err != nil {
		return nil, err
	}
	a.Files = len(members) - 1 // FILES.json is one of the members; MANIFEST.json is not hashed
	if haveRuntime {
		v, found, err := requiresAnsible(runtime)
		switch {
		case err != nil:
			a.Warnings = append(a.Warnings, err.Error()+"; requires_ansible is left empty")
		case found:
			a.RequiresAnsible = v
		}
	}
	if a.ReadmeName != "" {
		if _, ok := members[a.ReadmeName]; !ok {
			a.ReadmeName = ""
		}
	}
	return a, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cleanName turns the name of an entry into the name FILES.json uses: no leading "./", no trailing slash. A directory "." is "". An
// absolute name, a ".." or "." segment inside, a backslash or a control character is refused.
func cleanName(n string) (string, error) {
	n = strings.TrimSuffix(strings.TrimPrefix(n, "./"), "/")
	if n == "" || n == "." {
		return "", nil
	}
	if strings.HasPrefix(n, "/") || strings.ContainsRune(n, '\\') || !printable(n) {
		return "", refuse(ReasonUnsafeName, "the entry name %q is not a relative path of the archive", short(n))
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", refuse(ReasonUnsafeName, "the entry name %q leaves the archive or has an empty segment", short(n))
		}
	}
	return path.Clean(n), nil
}

func short(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

// manifestDoc is MANIFEST.json (R-ANS-010).
type manifestDoc struct {
	CollectionInfo   map[string]json.RawMessage `json:"collection_info"`
	FileManifestFile struct {
		Name         string `json:"name"`
		FType        string `json:"ftype"`
		ChksumType   string `json:"chksum_type"`
		ChksumSHA256 string `json:"chksum_sha256"`
	} `json:"file_manifest_file"`
}

func readManifest(doc []byte, filesDigest string, lim untrusted.Limits) (*Archive, error) {
	var m manifestDoc
	if err := untrusted.UnmarshalJSON(doc, lim, &m); err != nil {
		if untrusted.IsTooLarge(err) {
			return nil, fromStream(err)
		}
		return nil, refuse(ReasonManifestInvalid, "%s is not a JSON document of the expected shape", ManifestName)
	}
	if m.CollectionInfo == nil {
		return nil, refuse(ReasonManifestInvalid, "%s has no collection_info", ManifestName)
	}
	f := m.FileManifestFile
	if f.Name != FilesName || f.FType != "file" || f.ChksumType != "sha256" {
		return nil, refuse(ReasonManifestInvalid, "file_manifest_file of %s does not describe %s with a sha256 digest", ManifestName, FilesName)
	}
	if !strings.EqualFold(f.ChksumSHA256, filesDigest) || len(f.ChksumSHA256) != sha256.Size*2 {
		return nil, refuse(ReasonFilesChecksum, "the sha256 of %s is not the one %s holds", FilesName, ManifestName)
	}
	a := &Archive{}
	var err error
	if a.Namespace, err = stringField(m.CollectionInfo, "namespace"); err != nil || !ValidPart(a.Namespace) {
		return nil, refuse(ReasonInvalidName, "the namespace %q is not valid: 1 to 64 characters of a-z, 0-9 and _, not starting with _, with a letter", short(a.Namespace))
	}
	if a.Name, err = stringField(m.CollectionInfo, "name"); err != nil || !ValidPart(a.Name) {
		return nil, refuse(ReasonInvalidName, "the collection name %q is not valid: 1 to 64 characters of a-z, 0-9 and _, not starting with _, with a letter", short(a.Name))
	}
	if a.Version, err = stringField(m.CollectionInfo, "version"); err != nil || !version.Valid(a.Version) {
		return nil, refuse(ReasonInvalidVersion, "the version %q is not a semantic version 2.0.0", short(a.Version))
	}
	if a.Dependencies, err = dependencies(m.CollectionInfo["dependencies"]); err != nil {
		return nil, err
	}
	a.Info = reduceInfo(m.CollectionInfo)
	if r, e := stringField(m.CollectionInfo, "readme"); e == nil {
		if n, e := cleanName(r); e == nil {
			a.ReadmeName = n
		}
	}
	return a, nil
}

// stringField reads a JSON string member; a missing member, null or a non-string is an error.
func stringField(m map[string]json.RawMessage, key string) (string, error) {
	var s string
	raw, ok := m[key]
	if !ok {
		return "", errors.New("missing")
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// dependencies reads collection_info.dependencies: an object of "<ns>.<name>" to a requirement string, at most [MaxDependencies].
// A missing member or null is no dependency.
func dependencies(raw json.RawMessage) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, refuse(ReasonInvalidDependencies, "dependencies is not an object")
	}
	if len(in) > MaxDependencies {
		return nil, refuse(ReasonInvalidDependencies, "dependencies has %d entries; at most %d are accepted", len(in), MaxDependencies)
	}
	for k, v := range in {
		if _, _, ok := SplitName(k); !ok {
			return nil, refuse(ReasonInvalidDependencies, "the dependency %q is not a collection name <namespace>.<name>", short(k))
		}
		var req string
		if err := json.Unmarshal(v, &req); err != nil || len(req) > maxRequirement || !printable(req) {
			return nil, refuse(ReasonInvalidDependencies, "the requirement of the dependency %q is not a short string", short(k))
		}
		out[k] = req
	}
	return out, nil
}

// reduceInfo keeps the members of collection_info that a version document's metadata has, in the order of [InfoKeys].
func reduceInfo(info map[string]json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for _, k := range InfoKeys() {
		raw, ok := info[k]
		if !ok {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		if err := json.Compact(&b, raw); err != nil {
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	return json.RawMessage(b.Bytes())
}

// filesDoc is FILES.json (R-ANS-010).
type filesDoc struct {
	Files []struct {
		Name         string  `json:"name"`
		FType        string  `json:"ftype"`
		ChksumType   *string `json:"chksum_type"`
		ChksumSHA256 *string `json:"chksum_sha256"`
	} `json:"files"`
}

// checkFiles holds FILES.json against the archive: every listed file is a member with the listed digest, and every member is listed.
func checkFiles(doc []byte, members map[string]string, lim untrusted.Limits) error {
	var f filesDoc
	if err := untrusted.UnmarshalJSON(doc, lim, &f); err != nil {
		if untrusted.IsTooLarge(err) {
			return fromStream(err)
		}
		return refuse(ReasonFilesInvalid, "%s is not a JSON document of the expected shape", FilesName)
	}
	listed := make(map[string]bool, len(f.Files))
	for _, e := range f.Files {
		name, err := cleanName(e.Name)
		if err != nil {
			return err
		}
		if listed[name] && name != "" {
			return refuse(ReasonFilesInvalid, "%s lists %q twice", FilesName, short(e.Name))
		}
		listed[name] = true
		switch e.FType {
		case "dir":
		case "file":
			if name == "" || e.ChksumType == nil || *e.ChksumType != "sha256" || e.ChksumSHA256 == nil {
				return refuse(ReasonFilesInvalid, "%s lists the file %q without a sha256 digest", FilesName, short(e.Name))
			}
			have, ok := members[name]
			switch {
			case !ok || name == FilesName:
				return refuse(ReasonMemberMissing, "%s lists the file %q, which the archive does not hold", FilesName, short(e.Name))
			case !strings.EqualFold(have, *e.ChksumSHA256):
				return refuse(ReasonMemberChecksum, "the sha256 of %q is not the one %s lists", short(e.Name), FilesName)
			}
		default:
			return refuse(ReasonFilesInvalid, "%s lists %q with the type %q; only file and dir are accepted", FilesName, short(e.Name), short(e.FType))
		}
	}
	var unlisted []string
	for name := range members {
		if name != FilesName && !listed[name] {
			unlisted = append(unlisted, name)
		}
	}
	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		return refuse(ReasonUnlistedMember, "the archive holds %q, which %s does not list", short(unlisted[0]), FilesName)
	}
	return nil
}
