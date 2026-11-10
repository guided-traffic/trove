package imagescan

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The codes of a scan that did not finish (R-VULN-034). Terminal ones are not retried until the manifest, the limits or the analyser change.
const (
	CodeLayerMissing     = "layer_missing"
	CodeImageTooLarge    = "image_too_large"
	CodeTooManyLayers    = "too_many_layers"
	CodeDecompressedSize = "decompressed_size"
	CodeTooManyEntries   = "too_many_entries"
	CodePathTraversal    = "path_traversal"
	CodeDigestMismatch   = "digest_mismatch"
	CodeManifestInvalid  = "manifest_invalid"
	CodeLayerFormat      = "layer_format"
	CodeTimeout          = "timeout"
	CodeAnalyserFailed   = "analyser_failed"
	CodeReportTooLarge   = "report_too_large"
)

// ScanError is a scan that did not finish for a reason that belongs to the image or the analyser, not to the worker.
type ScanError struct {
	Code, Detail string
	// Terminal says that another attempt on the same bytes cannot succeed.
	Terminal bool
	// Defer, when set, says that the scan did not start for lack of room: it is due again after this long and no attempt is spent.
	Defer time.Duration
}

func (e *ScanError) Error() string { return e.Code + ": " + e.Detail }

// BlobReader reads the content of an image of a repository from the object store.
type BlobReader interface {
	// ReadManifest returns the bytes of a manifest, at most limit.
	ReadManifest(ctx context.Context, ref ImageRef, limit int64) ([]byte, error)
	// OpenBlob opens a blob of the image; found is false when the repository does not hold it (a remote repository whose client had the
	// layer already).
	OpenBlob(ctx context.Context, ref ImageRef, digest string) (rc io.ReadCloser, size int64, found bool, err error)
}

// ImageRef names one manifest of an image in a repository.
type ImageRef struct {
	RepoID int64
	Repo   string
	Name   string
	Digest [32]byte
}

// DigestString is the digest as the registry spells it.
func (r ImageRef) DigestString() string { return "sha256:" + hex.EncodeToString(r.Digest[:]) }

// ContentLimits bound what is read of an image before the analyser sees it (R-VULN-036).
type ContentLimits struct {
	// MaxLayers is the largest number of layers.
	MaxLayers int
	// MaxImageBytes bounds the compressed size of the config and the layers together.
	MaxImageBytes int64
	// MaxUncompressedBytes bounds what the layers decompress to, together.
	MaxUncompressedBytes int64
	// MaxEntries bounds the tar entries of all layers together.
	MaxEntries int64
}

// maxManifestBytes matches the limit of the registry (R-OCI-020); maxConfigBytes bounds the image configuration held in the layout.
const maxManifestBytes = 4 << 20

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type imageManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}

// buildLayout writes the manifest, its config and its layers as an OCI layout under dir, verifying every blob against its digest and
// checking every layer against the limits before the analyser reads it. A blob the repository does not hold ends the build with
// [CodeLayerMissing], which is not terminal: a pull of a remote image brings its layers after its manifest. admit, when not nil, is called
// with the compressed size of the config and the layers once the manifest passed its checks and before the first blob is written; its error
// ends the build, so the room for the blobs is held before they exist (Q-VULN-145).
func buildLayout(ctx context.Context, src BlobReader, ref ImageRef, dir string, lim ContentLimits, admit func(total int64) error) error {
	raw, err := src.ReadManifest(ctx, ref, maxManifestBytes)
	if err != nil {
		return err
	}
	var m imageManifest
	if err := untrusted.UnmarshalJSON(raw, untrusted.DefaultLimits().Tighten(untrusted.Limits{DocumentBytes: maxManifestBytes}), &m); err != nil {
		return &ScanError{Code: CodeManifestInvalid, Detail: "the manifest cannot be read", Terminal: true}
	}
	total, err := checkManifest(m, lim)
	if err != nil {
		return err
	}
	if admit != nil {
		if err := admit(total); err != nil {
			return err
		}
	}
	blobs := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		return err
	}
	if err := copyBlob(ctx, src, ref, m.Config, blobs); err != nil {
		return err
	}
	budget := &entryBudget{bytes: lim.MaxUncompressedBytes, entries: lim.MaxEntries}
	for _, l := range m.Layers {
		if err := copyBlob(ctx, src, ref, l, blobs); err != nil {
			return err
		}
		if err := checkLayer(ctx, filepath.Join(blobs, hexOf(l.Digest)), budget); err != nil {
			return err
		}
	}
	if err := writeBlob(filepath.Join(blobs, hexOf(ref.DigestString())), raw); err != nil {
		return err
	}
	index, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": ref.DigestString(), "size": len(raw),
		}},
	})
	if err != nil {
		return err
	}
	if err := writeBlob(filepath.Join(dir, "index.json"), index); err != nil {
		return err
	}
	return writeBlob(filepath.Join(dir, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`))
}

func writeBlob(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }

func hexOf(digest string) string { return strings.TrimPrefix(digest, "sha256:") }

// checkManifest returns the compressed size of the config and the layers together.
func checkManifest(m imageManifest, lim ContentLimits) (int64, error) {
	if !validDigest(m.Config.Digest) {
		return 0, &ScanError{Code: CodeManifestInvalid, Detail: "the manifest names no valid config digest", Terminal: true}
	}
	if len(m.Layers) > lim.MaxLayers {
		return 0, &ScanError{Code: CodeTooManyLayers, Detail: fmt.Sprintf("the image has %d layers, the limit is %d", len(m.Layers), lim.MaxLayers), Terminal: true}
	}
	total := m.Config.Size
	for _, l := range m.Layers {
		if !validDigest(l.Digest) {
			return 0, &ScanError{Code: CodeManifestInvalid, Detail: "the manifest names a layer without a valid digest", Terminal: true}
		}
		if l.Size < 0 {
			return 0, &ScanError{Code: CodeManifestInvalid, Detail: "the manifest names a layer of negative size", Terminal: true}
		}
		total += l.Size
	}
	if total > lim.MaxImageBytes {
		return 0, &ScanError{Code: CodeImageTooLarge, Detail: fmt.Sprintf("the image is %d bytes compressed, the limit is %d", total, lim.MaxImageBytes), Terminal: true}
	}
	return total, nil
}

func validDigest(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}

// copyBlob copies one blob into the layout and checks that its bytes are the digest the manifest names and no more than its size.
func copyBlob(ctx context.Context, src BlobReader, ref ImageRef, d descriptor, blobs string) error {
	rc, _, found, err := src.OpenBlob(ctx, ref, d.Digest)
	if err != nil {
		return err
	}
	if !found {
		return &ScanError{Code: CodeLayerMissing, Detail: "the repository does not hold " + d.Digest + " yet"}
	}
	defer func() { _ = rc.Close() }()
	target := filepath.Join(blobs, hexOf(d.Digest))
	if filepath.Base(target) != hexOf(d.Digest) {
		return &ScanError{Code: CodeManifestInvalid, Detail: "a digest is not a file name", Terminal: true}
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // a digest checked above, below the scan's own directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(contextReader{ctx, rc}, d.Size+1))
	if err != nil {
		return err
	}
	if n != d.Size || "sha256:"+hex.EncodeToString(sum.Sum(nil)) != d.Digest {
		return &ScanError{Code: CodeDigestMismatch, Detail: d.Digest + " does not have the bytes the manifest names", Terminal: true}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, context.Cause(c.ctx)
	}
	return c.r.Read(p)
}

// entryBudget is what is left of the limits of the decompressed layers, across all layers of the image.
type entryBudget struct {
	bytes, entries int64
}

// checkLayer reads a layer the way the analyser will, within the limits: it decompresses the stream, walks the tar and refuses an entry
// whose name climbs out of the root, before any bytes of it are read by the analyser. This is where a decompression bomb ends (R-VULN-036).
func checkLayer(ctx context.Context, path string, budget *entryBudget) error {
	f, err := os.Open(path) //nolint:gosec // a path under the scan's own scratch directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	stream, closeStream, err := decompressor(br)
	if err != nil {
		return err
	}
	defer closeStream()
	counter := &countingReader{r: stream, left: budget.bytes}
	tr := tar.NewReader(contextReader{ctx, counter})
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if counter.exceeded {
				return &ScanError{Code: CodeDecompressedSize, Detail: "the layers decompress to more than the limit", Terminal: true}
			}
			return &ScanError{Code: CodeLayerFormat, Detail: "a layer is not a tar stream: " + err.Error(), Terminal: true}
		}
		budget.entries--
		if budget.entries < 0 {
			return &ScanError{Code: CodeTooManyEntries, Detail: "the layers hold more entries than the limit", Terminal: true}
		}
		// A symbolic link may point anywhere: the analyser reads the tar and does not follow it on a disk. A hard link names another entry of
		// the archive, so it must stay inside.
		if traverses(h.Name) || (h.Typeflag == tar.TypeLink && traverses(h.Linkname)) {
			return &ScanError{Code: CodePathTraversal, Detail: "a layer names a path outside the image root", Terminal: true}
		}
		if _, err := io.Copy(io.Discard, contextReader{ctx, tr}); err != nil {
			if counter.exceeded {
				return &ScanError{Code: CodeDecompressedSize, Detail: "the layers decompress to more than the limit", Terminal: true}
			}
			return err
		}
	}
	budget.bytes = counter.left
	return nil
}

// traverses reports whether a tar name climbs out of the root: a NUL, or a ".." segment in either kind of slash.
func traverses(name string) bool {
	if strings.Contains(name, "\x00") {
		return true
	}
	return slices.Contains(strings.Split(strings.ReplaceAll(name, "\\", "/"), "/"), "..")
}

// decompressor opens a gzip or zstd stream by its magic bytes and passes anything else through as a plain tar.
func decompressor(br *bufio.Reader) (io.Reader, func(), error) {
	magic, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, &ScanError{Code: CodeLayerFormat, Detail: "a layer cannot be read", Terminal: true}
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, nil, &ScanError{Code: CodeLayerFormat, Detail: "a layer is not valid gzip", Terminal: true}
		}
		return zr, func() { _ = zr.Close() }, nil
	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		zr, err := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<31))
		if err != nil {
			return nil, nil, &ScanError{Code: CodeLayerFormat, Detail: "a layer is not valid zstd", Terminal: true}
		}
		return zr, zr.Close, nil
	}
	return br, func() {}, nil
}

// countingReader ends with an error when more than left bytes have been read, the budget of the decompressed size.
type countingReader struct {
	r        io.Reader
	left     int64
	exceeded bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		c.exceeded = true
		return 0, errors.New("the decompressed size exceeds the limit")
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
