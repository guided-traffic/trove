// Package manifest reads the manifests a registry stores (R-OCI-013, R-OCI-020): the OCI image manifest and image index and the
// Docker image manifest (schema 2) and manifest list. A manifest is untrusted input. [Parse] bounds it, reads it with a strict
// reader that refuses what two readers could interpret differently (see tree.go), checks the shape of its type and every descriptor
// in it, and computes the digest over the exact bytes it was given; it decides nothing about the repository (which blobs exist, who
// may push), and it never stores or fetches anything. Nothing of a manifest is ever used as a file-system path: the digests that
// become path segments are validated by the grammar before they leave this package.
//
// The package imports the standard library, errs and the untrusted-input helpers only.
package manifest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The media types a registry accepts (R-OCI-013 step 2).
const (
	MediaTypeImageManifest  = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeImageIndex     = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Docker schema 1, refused with a message of its own (Q-OCI-8).
const (
	mediaTypeSchema1       = "application/vnd.docker.distribution.manifest.v1+json"
	mediaTypeSchema1Signed = "application/vnd.docker.distribution.manifest.v1+prettyjws"
)

// The layer types that are not distributed through a registry (foreign layers, Q-OCI-10).
const (
	MediaTypeDockerForeignLayer = "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"
	mediaTypeOCINonDistributed  = "application/vnd.oci.image.layer.nondistributable.v1.tar"
)

// The bounds of R-OCI-020 that are not the descriptor's.
const (
	// MaxDescriptors is the number of descriptors one manifest may name.
	MaxDescriptors = 10_000
	// MaxURLs and MaxURLBytes bound the urls of one foreign layer.
	MaxURLs     = 16
	MaxURLBytes = 2048
	// MaxPlatformBytes is the longest "os/arch/variant" of the references table (oci_references.platform).
	MaxPlatformBytes = 128
	// MaxMediaTypeBytes is the column bound of oci_manifests.media_type and artifact_type.
	MaxMediaTypeBytes = 255
	// MaxAnnotationBytes is the bound of the annotations a manifest row keeps (R-OCI-007).
	MaxAnnotationBytes = 64 << 10
)

// Type is the family a manifest belongs to.
type Type uint8

const (
	// TypeImage is an image manifest: a config and layers (OCI or Docker schema 2).
	TypeImage Type = iota + 1
	// TypeIndex is an index of manifests: an OCI image index or a Docker manifest list.
	TypeIndex
)

// Platform is the platform an index entry names for its child.
type Platform struct{ OS, Architecture, Variant string }

// String is "os/arch" or "os/arch/variant", the form of oci_references.platform.
func (p Platform) String() string {
	if p.Variant == "" {
		return p.OS + "/" + p.Architecture
	}
	return p.OS + "/" + p.Architecture + "/" + p.Variant
}

// Descriptor is a content descriptor: what a manifest names.
type Descriptor struct {
	MediaType string
	// Digest is "sha256:" and 64 lower-case hex digits.
	Digest string
	Size   int64
	// URLs mark a foreign layer.
	URLs         []string
	ArtifactType string
	// Platform is set on an index entry that names one.
	Platform *Platform
}

// Foreign reports whether the descriptor names a layer the registry is not asked to hold: it has urls, or a media type of a layer
// that is not distributed (Q-OCI-10).
func (d Descriptor) Foreign() bool {
	if len(d.URLs) > 0 {
		return true
	}
	switch d.MediaType {
	case MediaTypeDockerForeignLayer, mediaTypeOCINonDistributed, mediaTypeOCINonDistributed + "+gzip", mediaTypeOCINonDistributed + "+zstd":
		return true
	}
	return false
}

// Manifest is a manifest that passed the checks of [Parse].
type Manifest struct {
	Type Type
	// MediaType is the media type of the document: the Content-Type without parameters, which the document's own mediaType field,
	// when it has one, equals.
	MediaType string
	// Digest is "sha256:<hex>" over the exact bytes, and Sum the same as raw bytes.
	Digest string
	Sum    [32]byte
	// Size is the length of the document.
	Size int64
	// Config and Layers are an image manifest's; Manifests an index's.
	Config    *Descriptor
	Layers    []Descriptor
	Manifests []Descriptor
	// Subject is the descriptor the manifest names as its subject (R-OCI-016); nil for none. It need not exist (Q-OCI-10).
	Subject *Descriptor
	// ArtifactType is the manifest's own artifactType, "" for none.
	ArtifactType string
	// Annotations are the manifest's own, not its descriptors'.
	Annotations map[string]string
}

// Kind of a reference.
type Kind uint8

const (
	// KindBlob is a config or a layer.
	KindBlob Kind = iota + 1
	// KindManifest is an index entry.
	KindManifest
)

// Reference is one distinct target of a manifest.
type Reference struct {
	Digest string
	Kind   Kind
	// Platform is the platform of the first index entry that names the target and has one; "" otherwise.
	Platform string
	// Required says the repository must hold the target for the manifest to be accepted: every target but a foreign layer
	// that no other descriptor of the manifest names as a distributable one (Q-OCI-10).
	Required bool
	// Size is the size the first descriptor that names the target declares.
	Size int64
}

// References lists what the manifest names, each digest once, in the order of first appearance: the config, the layers, or the
// index entries. The subject is no reference.
func (m *Manifest) References() []Reference {
	var out []Reference
	at := map[string]int{}
	add := func(d Descriptor, kind Kind) {
		i, seen := at[d.Digest]
		if !seen {
			at[d.Digest] = len(out)
			ref := Reference{Digest: d.Digest, Kind: kind, Required: !d.Foreign(), Size: d.Size}
			if d.Platform != nil {
				ref.Platform = d.Platform.String()
			}
			out = append(out, ref)
			return
		}
		if !d.Foreign() {
			out[i].Required = true
		}
		if d.Platform != nil && out[i].Platform == "" {
			out[i].Platform = d.Platform.String()
		}
	}
	if m.Config != nil {
		add(*m.Config, KindBlob)
	}
	for _, d := range m.Layers {
		add(d, KindBlob)
	}
	for _, d := range m.Manifests {
		add(d, KindManifest)
	}
	return out
}

// ContentSize is the summed size of the config and the layers of an image manifest, saturated at the largest int64 (the sizes are
// the pusher's claims, and the sum of ten thousand of them must not wrap).
func (m *Manifest) ContentSize() int64 {
	var sum int64
	add := func(n int64) {
		if sum > maxInt64-n {
			sum = maxInt64
			return
		}
		sum += n
	}
	if m.Config != nil {
		add(m.Config.Size)
	}
	for _, d := range m.Layers {
		add(d.Size)
	}
	return sum
}

const maxInt64 = int64(^uint64(0) >> 1)

// EffectiveArtifactType is the artifact type a referrers answer reports (R-OCI-016): the manifest's own, else the config's media
// type of an image manifest, else "".
func (m *Manifest) EffectiveArtifactType() string {
	switch {
	case m.ArtifactType != "":
		return m.ArtifactType
	case m.Config != nil:
		return m.Config.MediaType
	}
	return ""
}

// RowAnnotations returns the annotations a manifest row keeps (R-OCI-007) as a JSON object, at most limit bytes of the form the
// database stores (keys sorted; ": " and ", " as separators, which is how jsonb prints), and whether anything was left out: an entry
// that does not fit after the entries before it in key order, and an entry whose key or value holds a NUL (jsonb cannot hold one).
// nil for a manifest without annotations.
func (m *Manifest) RowAnnotations(limit int) (raw []byte, cut bool) {
	if len(m.Annotations) == 0 {
		return nil, false
	}
	keys := make([]string, 0, len(m.Annotations))
	for k := range m.Annotations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		v := m.Annotations[k]
		if strings.IndexByte(k, 0) >= 0 || strings.IndexByte(v, 0) >= 0 {
			cut = true
			continue
		}
		entry := quoteJSON(k) + ": " + quoteJSON(v)
		// The text so far, the separator of an entry that is not the first, the entry and the closing brace: the length of the text
		// jsonb prints for the object, which the CHECK of the column bounds.
		sep := 0
		if !first {
			sep = 2
		}
		if b.Len()+sep+len(entry)+1 > limit {
			cut = true
			break
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		b.WriteString(entry)
	}
	b.WriteByte('}')
	if first {
		return nil, cut
	}
	return []byte(b.String()), cut
}

// quoteJSON writes s as a JSON string. It escapes more than jsonb prints (U+2028, U+2029, the HTML characters), never less, so a
// length computed from it bounds the stored text.
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == '<' || r == '>' || r == '&':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func invalidf(format string, a ...any) error {
	return errs.E(errs.Invalid, errs.CodeOCIManifestInvalid, fmt.Sprintf(format, a...))
}

func tooLarge(detail string) error {
	return errs.E(errs.TooLarge, errs.CodeOCISizeInvalid, detail)
}

// quote shows an attacker-chosen string in an error message: bounded and escaped.
func quote(s string) string {
	if len(s) > 48 {
		s = s[:48]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		return strconv.Quote(s) + "..."
	}
	return strconv.Quote(s)
}

// Parse reads a manifest pushed (or fetched) as contentType. data is the whole document, exactly as received: the digest is the
// SHA-256 of it. lim are the limits of the descriptor of the format (the document bound, the nesting, the number of values); a
// bound that is crossed is errs.TooLarge with the code SIZE_INVALID, anything else that is wrong is errs.Invalid with the code
// MANIFEST_INVALID.
func Parse(data []byte, contentType string, lim untrusted.Limits) (*Manifest, error) {
	if int64(len(data)) > lim.Bound() {
		return nil, tooLarge(fmt.Sprintf("the manifest is larger than %d bytes", lim.Bound()))
	}
	if err := untrusted.CheckJSON(data, lim); err != nil {
		if untrusted.IsTooLarge(err) {
			var e *errs.Error
			if errors.As(err, &e) {
				return nil, tooLarge(e.Detail)
			}
			return nil, tooLarge("the manifest exceeds a limit of the registry")
		}
		return nil, invalidf("the manifest is not valid JSON")
	}
	mt, err := MediaTypeOf(contentType)
	if err != nil {
		return nil, err
	}
	depth, values := lim.JSONDepth, lim.JSONMembers
	if depth <= 0 {
		depth = untrusted.DefaultLimits().JSONDepth
	}
	if values <= 0 {
		values = untrusted.DefaultLimits().JSONMembers
	}
	root, err := readTree(data, depth, values)
	if err != nil {
		return nil, err
	}
	if root.kind != kindObject {
		return nil, invalidf("the manifest is not a JSON object")
	}
	m := &Manifest{MediaType: mt, Sum: sha256.Sum256(data), Size: int64(len(data))}
	m.Digest = "sha256:" + hex.EncodeToString(m.Sum[:])
	p := &parser{}
	switch mt {
	case MediaTypeImageManifest, MediaTypeDockerManifest:
		m.Type = TypeImage
		err = p.image(root, m)
	default:
		m.Type = TypeIndex
		err = p.index(root, m)
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// MediaTypeOf reduces a Content-Type to the media type of a manifest the registry accepts, before the body is read: the parameters
// are dropped, a type of Docker schema 1 or any type that is no manifest type is MANIFEST_INVALID.
func MediaTypeOf(contentType string) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		return "", invalidf("the manifest has no Content-Type")
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", invalidf("the Content-Type of the manifest is not a media type")
	}
	switch mt {
	case MediaTypeImageManifest, MediaTypeImageIndex, MediaTypeDockerManifest, MediaTypeDockerList:
		return mt, nil
	case mediaTypeSchema1, mediaTypeSchema1Signed:
		return "", invalidf("manifests of Docker schema 1 are not accepted; rebuild the image with a current Docker or Buildx")
	}
	return "", invalidf("the media type %s is not a manifest type this registry accepts", quote(mt))
}

type parser struct{ descriptors int }

// Keys that name the same thing under another case are ambiguous: a reader that compares without regard to case
// (the standard decoder of most clients does) sees a field this reader does not. They are refused wherever a known key is read.
func refuseLookalikes(where string, obj map[string]node, known ...string) error {
	for key := range obj {
		for _, k := range known {
			if key != k && strings.EqualFold(key, k) {
				return invalidf("%s has the key %s, which only differs in case from %q", where, quote(key), k)
			}
		}
	}
	return nil
}

func (p *parser) common(root node, m *Manifest, known ...string) (map[string]node, error) {
	obj := root.obj
	known = append(known, "schemaVersion", "mediaType", "artifactType", "subject", "annotations")
	if err := refuseLookalikes("the manifest", obj, known...); err != nil {
		return nil, err
	}
	if _, ok := obj["fsLayers"]; ok {
		return nil, invalidf("the document has fsLayers, which only a manifest of Docker schema 1 has")
	}
	v, ok := obj["schemaVersion"]
	if !ok {
		return nil, invalidf("the manifest has no schemaVersion")
	}
	if v.kind != kindNumber || v.s != "2" {
		return nil, invalidf("the schemaVersion of the manifest is not 2")
	}
	if v, ok := obj["mediaType"]; ok {
		if v.kind != kindString || v.s != m.MediaType {
			return nil, invalidf("the mediaType of the document does not equal its Content-Type %s", m.MediaType)
		}
	} else if m.MediaType == MediaTypeDockerManifest || m.MediaType == MediaTypeDockerList {
		return nil, invalidf("the Docker manifest has no mediaType")
	}
	if v, ok := obj["artifactType"]; ok {
		if v.kind != kindString || !ValidMediaType(v.s) {
			return nil, invalidf("the artifactType is not a media type")
		}
		m.ArtifactType = v.s
	}
	if v, ok := obj["subject"]; ok {
		d, err := p.descriptor(v, "the subject", false)
		if err != nil {
			return nil, err
		}
		m.Subject = &d
	}
	if v, ok := obj["annotations"]; ok {
		a, err := stringMap(v, "the annotations")
		if err != nil {
			return nil, err
		}
		m.Annotations = a
	}
	return obj, nil
}

func (p *parser) image(root node, m *Manifest) error {
	obj, err := p.common(root, m, "config", "layers", "manifests", "fsLayers")
	if err != nil {
		return err
	}
	if _, ok := obj["manifests"]; ok {
		return invalidf("an image manifest has no manifests; the document looks like an index")
	}
	cfg, ok := obj["config"]
	if !ok {
		return invalidf("the image manifest has no config")
	}
	c, err := p.descriptor(cfg, "the config", false)
	if err != nil {
		return err
	}
	m.Config = &c
	layers, ok := obj["layers"]
	if !ok || layers.kind != kindArray {
		return invalidf("the image manifest has no layers array")
	}
	m.Layers = make([]Descriptor, 0, len(layers.arr))
	for i, l := range layers.arr {
		d, err := p.descriptor(l, "layer "+strconv.Itoa(i), false)
		if err != nil {
			return err
		}
		m.Layers = append(m.Layers, d)
	}
	return nil
}

func (p *parser) index(root node, m *Manifest) error {
	obj, err := p.common(root, m, "manifests", "config", "layers", "fsLayers")
	if err != nil {
		return err
	}
	for _, k := range []string{"config", "layers"} {
		if _, ok := obj[k]; ok {
			return invalidf("an index has no %s; the document looks like an image manifest", k)
		}
	}
	list, ok := obj["manifests"]
	if !ok || list.kind != kindArray {
		return invalidf("the index has no manifests array")
	}
	m.Manifests = make([]Descriptor, 0, len(list.arr))
	for i, e := range list.arr {
		d, err := p.descriptor(e, "manifest "+strconv.Itoa(i), true)
		if err != nil {
			return err
		}
		m.Manifests = append(m.Manifests, d)
	}
	return nil
}

// descriptor reads a content descriptor: a valid digest, a non-negative size and a media type are required (R-OCI-013 step 3).
func (p *parser) descriptor(n node, where string, platform bool) (Descriptor, error) {
	if n.kind != kindObject {
		return Descriptor{}, invalidf("%s is not an object", where)
	}
	p.descriptors++
	if p.descriptors > MaxDescriptors {
		return Descriptor{}, tooLarge(fmt.Sprintf("the manifest names more than %d descriptors", MaxDescriptors))
	}
	if err := refuseLookalikes(where, n.obj, "mediaType", "digest", "size", "urls", "annotations", "data", "artifactType", "platform"); err != nil {
		return Descriptor{}, err
	}
	var d Descriptor
	mt, ok := n.obj["mediaType"]
	if !ok || mt.kind != kindString || !ValidMediaType(mt.s) {
		return Descriptor{}, invalidf("%s has no valid mediaType", where)
	}
	d.MediaType = mt.s
	dg, ok := n.obj["digest"]
	if !ok || dg.kind != kindString || !ValidDigest(dg.s) {
		return Descriptor{}, invalidf("%s has no valid sha256 digest", where)
	}
	d.Digest = dg.s
	sz, ok := n.obj["size"]
	if !ok || sz.kind != kindNumber {
		return Descriptor{}, invalidf("%s has no size", where)
	}
	size, err := integer(sz.s)
	if err != nil {
		return Descriptor{}, invalidf("%s has a size that is not a non-negative integer", where)
	}
	d.Size = size
	if u, ok := n.obj["urls"]; ok {
		if d.URLs, err = urls(u, where); err != nil {
			return Descriptor{}, err
		}
	}
	if a, ok := n.obj["annotations"]; ok {
		if _, err := stringMap(a, where+" annotations"); err != nil {
			return Descriptor{}, err
		}
	}
	if v, ok := n.obj["data"]; ok {
		if err := checkData(v, d, where); err != nil {
			return Descriptor{}, err
		}
	}
	if v, ok := n.obj["artifactType"]; ok {
		if v.kind != kindString || !ValidMediaType(v.s) {
			return Descriptor{}, invalidf("%s has an artifactType that is not a media type", where)
		}
		d.ArtifactType = v.s
	}
	if pl, ok := n.obj["platform"]; ok && platform {
		if d.Platform, err = parsePlatform(pl, where); err != nil {
			return Descriptor{}, err
		}
	}
	return d, nil
}

// checkData verifies the embedded content of a descriptor (the image specification's `data` field): standard base64 of exactly the
// bytes the descriptor's size and digest name. A client that trusts the embedded bytes would otherwise read content the digest does not
// vouch for.
func checkData(n node, d Descriptor, where string) error {
	if n.kind != kindString {
		return invalidf("%s has data that is not a string", where)
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(n.s)
	if err != nil {
		return invalidf("%s has data that is not standard base64", where)
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != d.Size || "sha256:"+hex.EncodeToString(sum[:]) != d.Digest {
		return invalidf("%s has data that is not the content its size and digest name", where)
	}
	return nil
}

// integer reads the text of a JSON number that is a non-negative integer in plain decimal form: no sign, no fraction, no exponent,
// no leading zero.
func integer(s string) (int64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errNotInteger
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, errNotInteger
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

var errNotInteger = fmt.Errorf("not an integer")

func urls(n node, where string) ([]string, error) {
	if n.kind != kindArray || len(n.arr) > MaxURLs {
		return nil, invalidf("%s has urls that are not a list of at most %d", where, MaxURLs)
	}
	out := make([]string, 0, len(n.arr))
	for _, e := range n.arr {
		if e.kind != kindString || len(e.s) > MaxURLBytes {
			return nil, invalidf("%s has a url that is not a string of at most %d bytes", where, MaxURLBytes)
		}
		u, err := url.Parse(e.s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return nil, invalidf("%s has a url that is not an absolute http or https URL without credentials", where)
		}
		out = append(out, e.s)
	}
	return out, nil
}

func stringMap(n node, where string) (map[string]string, error) {
	if n.kind != kindObject {
		return nil, invalidf("%s are not an object", where)
	}
	out := make(map[string]string, len(n.obj))
	for k, v := range n.obj {
		if v.kind != kindString {
			return nil, invalidf("%s hold a value that is not a string", where)
		}
		out[k] = v.s
	}
	return out, nil
}

func parsePlatform(n node, where string) (*Platform, error) {
	if n.kind != kindObject {
		return nil, invalidf("%s has a platform that is not an object", where)
	}
	if err := refuseLookalikes(where+" platform", n.obj, "os", "architecture", "variant", "os.version", "os.features", "features"); err != nil {
		return nil, err
	}
	part := func(key string, required bool) (string, error) {
		v, ok := n.obj[key]
		if !ok {
			if required {
				return "", invalidf("%s has a platform without %s", where, key)
			}
			return "", nil
		}
		if v.kind != kindString || !validPlatformPart(v.s) {
			return "", invalidf("%s has a platform %s that is not a short plain name", where, key)
		}
		return v.s, nil
	}
	var p Platform
	var err error
	if p.OS, err = part("os", true); err != nil {
		return nil, err
	}
	if p.Architecture, err = part("architecture", true); err != nil {
		return nil, err
	}
	if p.Variant, err = part("variant", false); err != nil {
		return nil, err
	}
	if len(p.String()) > MaxPlatformBytes {
		return nil, invalidf("%s has a platform longer than %d bytes", where, MaxPlatformBytes)
	}
	return &p, nil
}

// validPlatformPart is [A-Za-z0-9][A-Za-z0-9._+-]{0,63}.
func validPlatformPart(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '+' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidDigest reports whether s is a digest the registry knows: "sha256:" and 64 lower-case hexadecimal digits (Q-OCI-9).
func ValidDigest(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || s[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ParseDigest returns the 32 bytes of a digest that [ValidDigest] accepts.
func ParseDigest(s string) ([32]byte, bool) {
	var out [32]byte
	if !ValidDigest(s) {
		return out, false
	}
	if _, err := hex.Decode(out[:], []byte(s[len("sha256:"):])); err != nil {
		return [32]byte{}, false
	}
	return out, true
}

// ValidMediaType reports whether s is a media type without parameters in the form RFC 6838 gives and the image specification
// requires of a descriptor: type "/" subtype, each of 1 to 127 characters starting with a letter or a digit, at most 255 bytes.
func ValidMediaType(s string) bool {
	typ, sub, ok := strings.Cut(s, "/")
	return ok && len(s) <= MaxMediaTypeBytes && mediaTypePart(typ) && mediaTypePart(sub)
}

func mediaTypePart(s string) bool {
	if s == "" || len(s) > 127 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && strings.IndexByte("!#$&^_.+-", c) >= 0:
		default:
			return false
		}
	}
	return true
}
