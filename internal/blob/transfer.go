package blob

import (
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 payload checksum for stores without additional checksums (R-STOR-006)
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"
)

const (
	maxParts = 10000
	mib      = 1 << 20
)

// putObject writes the content of sp to its key (R-STOR-006): one request up to
// storage.s3.multipart.threshold, a multipart upload from the spool file above
// it. Failures map to the 503 of R-STOR-036 when the store is the cause.
func (s *Store) putObject(ctx context.Context, sp *Spooled) error {
	key := s.Key(sp.SHA256)
	var err error
	if sp.Size <= int64(s.cfg.S3.Multipart.Threshold) {
		err = s.objects.Put(ctx, key, sp.Reader(), sp.Size, s.wholeChecksum(sp))
	} else {
		err = s.putMultipart(ctx, key, sp)
	}
	if err != nil {
		return mapStoreErr(err)
	}
	return nil
}

// mapStoreErr turns a store failure into the error a request answers with. The
// original error stays the cause.
func mapStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrCircuitOpen), IsTransient(err), IsConfig(err):
		return storeUnavailable(err)
	}
	return err
}

// wholeChecksum is the payload checksum of a single request. SHA-256 and MD5 of
// the whole content are known from the hashing pass.
func (s *Store) wholeChecksum(sp *Spooled) Checksum {
	switch s.cfg.S3.PayloadChecksum {
	case "sha256":
		return Checksum{Algorithm: "sha256", Value: sp.SHA256[:]}
	case "md5":
		return Checksum{Algorithm: "md5", Value: sp.MD5[:]}
	}
	return Checksum{}
}

// PartSize is the part size of a multipart upload of size bytes: the configured
// size, raised so that the content fits into 10,000 parts, rounded up to whole
// MiB (R-STOR-006).
func PartSize(size, configured int64) int64 {
	ps := configured
	if need := (size + maxParts - 1) / maxParts; need > ps {
		ps = (need + mib - 1) / mib * mib
	}
	return ps
}

func (s *Store) putMultipart(ctx context.Context, key string, sp *Spooled) (err error) {
	algorithm := s.cfg.S3.PayloadChecksum
	if algorithm == "none" {
		algorithm = ""
	}
	partSize := PartSize(sp.Size, int64(s.cfg.S3.Multipart.PartSize))
	n := int((sp.Size + partSize - 1) / partSize)

	id, err := s.objects.CreateMultipart(ctx, key, algorithm)
	if err != nil {
		return err
	}
	defer func() {
		if err == nil {
			return
		}
		// The request context may be what failed: abort with one of our own.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if aerr := s.objects.AbortMultipart(actx, key, id); aerr != nil {
			s.log.Warn("could not abort a multipart upload; the orphan sweep will", "key", key, "upload_id", id, "error", aerr)
		}
	}()

	parts := make([]Part, n)
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	local := make(chan struct{}, max(1, s.cfg.S3.Multipart.Concurrency))
	fail := func(e error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = e
			cancel()
		}
		mu.Unlock()
	}
launch:
	for i := 0; i < n; i++ {
		select {
		case local <- struct{}{}:
		case <-pctx.Done():
			break launch
		}
		select {
		case s.parts <- struct{}{}:
		case <-pctx.Done():
			<-local
			break launch
		}
		off := int64(i) * partSize
		size := min(partSize, sp.Size-off)
		wg.Add(1)
		go func(i int, off, size int64) {
			defer wg.Done()
			defer func() { <-s.parts; <-local }()
			p, perr := s.uploadPart(pctx, key, id, i+1, sp, off, size, algorithm)
			if perr != nil {
				fail(perr)
				return
			}
			parts[i] = p
		}(i, off, size)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.objects.CompleteMultipart(ctx, key, id, parts); err != nil {
		return err
	}
	info, err := s.objects.Head(ctx, key)
	if err != nil {
		return err
	}
	if info.Size >= 0 && info.Size != sp.Size { // -1: the store did not say
		// The completed object is not what was sent. It is not deleted here: a
		// concurrent uploader may have written the right bytes to the same key.
		return fmt.Errorf("object %s has %d bytes after the multipart upload, expected %d", key, info.Size, sp.Size)
	}
	return nil
}

// uploadPart sends one part read directly from the spool file.
func (s *Store) uploadPart(ctx context.Context, key, id string, number int, sp *Spooled, off, size int64, algorithm string) (Part, error) {
	var sum Checksum
	if algorithm != "" {
		var h hash.Hash
		if algorithm == "sha256" {
			h = sha256.New()
		} else {
			h = md5.New() //nolint:gosec // see imports
		}
		if _, err := io.Copy(h, sp.Section(off, size)); err != nil {
			return Part{}, fmt.Errorf("hash part %d: %w", number, err)
		}
		sum = Checksum{Algorithm: algorithm, Value: h.Sum(nil)}
	}
	p, err := s.objects.UploadPart(ctx, key, id, number, sp.Section(off, size), size, sum)
	if err != nil {
		return Part{}, err
	}
	p.Number, p.Size, p.Checksum = number, size, sum
	return p, nil
}
