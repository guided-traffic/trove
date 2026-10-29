package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// This file is the object-store check of R-STOR-035. The startup check is what
// a replica runs before its first readiness; the conformance check is the
// operator's evidence that a store meets assumptions A1 and A2 (and the
// behaviour trove relies on besides): `trove admin storage check`, and the
// integration tier against every store of the CI matrix (R-STOR-030). It
// changes nothing outside <prefix>/_probe/.

// ConformanceOptions configure a check run.
type ConformanceOptions struct {
	// Prefix is storage.s3.prefix.
	Prefix string
	// Replica names the probe objects: the replica ID, or any unique string.
	Replica string
	// HTTP fetches presigned URLs. It must come from internal/outbound.
	HTTP *http.Client
}

// ConformanceEnv is what a check works with.
type ConformanceEnv struct {
	Store ObjectStore
	Opts  ConformanceOptions
	// created are the probe keys to delete when the check ends.
	mu      sync.Mutex
	created []string
	uploads []MultipartUpload
}

// Key returns a new probe key; the check's cleanup deletes it.
func (e *ConformanceEnv) Key(name string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	k := probePrefix(e.Opts.Prefix) + e.Opts.Replica + "." + name + "." + hex.EncodeToString(b[:])
	e.mu.Lock()
	e.created = append(e.created, k)
	e.mu.Unlock()
	return k
}

func probePrefix(prefix string) string {
	if prefix == "" {
		return "_probe/"
	}
	return prefix + "/_probe/"
}

func (e *ConformanceEnv) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	e.mu.Lock()
	keys, ups := e.created, e.uploads
	e.mu.Unlock()
	for _, u := range ups {
		_ = e.Store.AbortMultipart(ctx, u.Key, u.UploadID)
	}
	for _, k := range keys {
		_ = e.Store.Delete(ctx, k)
	}
}

// ConformanceCheck is one named check.
type ConformanceCheck struct {
	// Name says what is checked.
	Name string
	// Assumption is the row of R-STOR-035's table the check backs, or "" for a
	// behaviour trove relies on besides.
	Assumption string
	// Optional marks a feature a store may lack, with a setting that works
	// around it (DeleteObjects: storage.gc.batch_delete: false).
	Optional bool
	Run      func(ctx context.Context, e *ConformanceEnv) error
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	Name       string        `json:"name"`
	Assumption string        `json:"assumption,omitempty"`
	Optional   bool          `json:"optional,omitempty"`
	OK         bool          `json:"ok"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"-"`
}

func payload(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func putBytes(ctx context.Context, s ObjectStore, key string, data []byte) error {
	return s.Put(ctx, key, bytes.NewReader(data), int64(len(data)), Checksum{})
}

func readAll(ctx context.Context, s ObjectStore, key string, rng *ByteRange) ([]byte, error) {
	body, _, err := s.Get(ctx, key, rng)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func wantMissing(ctx context.Context, s ObjectStore, key string) error {
	if _, err := s.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		return wrongAnswer("Head of a missing key", err)
	}
	if _, _, err := s.Get(ctx, key, nil); !errors.Is(err, ErrNotFound) {
		return wrongAnswer("Get of a missing key", err)
	}
	return nil
}

// wrongAnswer reports a call that should have failed with ErrNotFound.
func wrongAnswer(what string, err error) error {
	if err == nil {
		return fmt.Errorf("%s succeeded, want ErrNotFound", what)
	}
	return fmt.Errorf("%s returned %w, want ErrNotFound", what, err)
}

func listHas(ctx context.Context, s ObjectStore, prefix, key string) (bool, error) {
	token := ""
	for {
		page, err := s.List(ctx, prefix, ListOptions{Token: token})
		if err != nil {
			return false, err
		}
		for _, o := range page.Objects {
			if o.Key == key {
				return true, nil
			}
		}
		if page.Next == "" {
			return false, nil
		}
		token = page.Next
	}
}

// ConformanceChecks returns the checks in the order they run.
func ConformanceChecks() []ConformanceCheck {
	return []ConformanceCheck{
		{Name: "write, read back, compare and delete a probe object", Assumption: "A1", Run: checkProbe},
		{Name: "list the blob prefix", Assumption: "A1", Run: checkListKeyPrefix},
		{Name: "a missing key reads as not found", Assumption: "A1", Run: checkMissing},
		{Name: "an overwrite is visible to the next read", Assumption: "A1", Run: checkOverwrite},
		{Name: "a new object is listed by the next list", Assumption: "A1", Run: checkListAfterWrite},
		{Name: "a deleted object is gone for the next read and list", Assumption: "A1", Run: checkReadAfterDelete},
		{Name: "deleting a missing key succeeds", Assumption: "A1", Run: checkDeleteMissing},
		{Name: "concurrent writes of identical bytes leave those bytes", Assumption: "A6", Run: checkConcurrentSameBytes},
		{Name: "a ranged read returns exactly the range", Run: checkRange},
		{Name: "a multipart object is invisible until completion and complete after", Assumption: "A2", Run: checkMultipart},
		{Name: "an aborted multipart upload leaves no object and no upload", Assumption: "A2", Run: checkAbort},
		{Name: "DeleteObjects removes several keys with one request", Optional: true, Run: checkDeleteMany},
		{Name: "a presigned URL serves the object", Run: checkPresign},
	}
}

// RunConformance runs every check and returns one result each. A failed check
// does not stop the others.
func RunConformance(ctx context.Context, store ObjectStore, opts ConformanceOptions) []CheckResult {
	var out []CheckResult
	for _, c := range ConformanceChecks() {
		out = append(out, RunCheck(ctx, store, opts, c))
	}
	return out
}

// RunCheck runs one check and removes what it created.
func RunCheck(ctx context.Context, store ObjectStore, opts ConformanceOptions, c ConformanceCheck) CheckResult {
	e := &ConformanceEnv{Store: store, Opts: opts}
	defer e.cleanup(ctx)
	start := time.Now()
	err := c.Run(ctx, e)
	r := CheckResult{Name: c.Name, Assumption: c.Assumption, Optional: c.Optional, OK: err == nil, Duration: time.Since(start)}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func checkProbe(ctx context.Context, e *ConformanceEnv) error {
	return probe(ctx, e.Store, e.Opts.Prefix, e.Opts.Replica)
}

// probe is the startup check: write, read back, compare, delete the probe
// object (R-STOR-035). The key is <prefix>/_probe/<replica>.
func probe(ctx context.Context, s ObjectStore, prefix, replica string) error {
	key := probePrefix(prefix) + replica
	data := payload(1024)
	if err := putBytes(ctx, s, key, data); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	got, err := readAll(ctx, s, key, nil)
	if err != nil {
		return fmt.Errorf("read back %s: %w", key, err)
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("read back %s: the bytes differ from the bytes written (assumption A1 or A2 does not hold)", key)
	}
	if err := s.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

func checkListKeyPrefix(ctx context.Context, e *ConformanceEnv) error {
	if _, err := e.Store.List(ctx, KeyPrefix(e.Opts.Prefix), ListOptions{Max: 1}); err != nil {
		return fmt.Errorf("list %s: %w", KeyPrefix(e.Opts.Prefix), err)
	}
	return nil
}

func checkMissing(ctx context.Context, e *ConformanceEnv) error {
	return wantMissing(ctx, e.Store, e.Key("missing"))
}

func checkOverwrite(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("overwrite")
	for i := 0; i < 5; i++ {
		data := payload(100 + i)
		if err := putBytes(ctx, e.Store, key, data); err != nil {
			return err
		}
		got, err := readAll(ctx, e.Store, key, nil)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, data) {
			return fmt.Errorf("round %d: the read after an overwrite returned the earlier content", i)
		}
		info, err := e.Store.Head(ctx, key)
		if err != nil {
			return err
		}
		if info.Size != int64(len(data)) {
			return fmt.Errorf("round %d: Head after an overwrite reports %d bytes, want %d", i, info.Size, len(data))
		}
	}
	return nil
}

func checkListAfterWrite(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("listed")
	if err := putBytes(ctx, e.Store, key, payload(10)); err != nil {
		return err
	}
	ok, err := listHas(ctx, e.Store, probePrefix(e.Opts.Prefix)+e.Opts.Replica+".listed.", key)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("the object is missing from the listing right after its write")
	}
	return nil
}

func checkReadAfterDelete(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("deleted")
	if err := putBytes(ctx, e.Store, key, payload(10)); err != nil {
		return err
	}
	if err := e.Store.Delete(ctx, key); err != nil {
		return err
	}
	if err := wantMissing(ctx, e.Store, key); err != nil {
		return fmt.Errorf("after delete: %w", err)
	}
	ok, err := listHas(ctx, e.Store, key, key)
	if err != nil {
		return err
	}
	if ok {
		return errors.New("the object is still listed right after its delete")
	}
	return nil
}

func checkDeleteMissing(ctx context.Context, e *ConformanceEnv) error {
	return e.Store.Delete(ctx, e.Key("never-written"))
}

func checkConcurrentSameBytes(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("same")
	data := payload(4096)
	var wg sync.WaitGroup
	errc := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errc <- putBytes(ctx, e.Store, key, data)
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			return err
		}
	}
	got, err := readAll(ctx, e.Store, key, nil)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, data) {
		return errors.New("the object differs from the bytes every writer sent")
	}
	return nil
}

func checkRange(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("range")
	data := payload(1000)
	if err := putBytes(ctx, e.Store, key, data); err != nil {
		return err
	}
	for _, r := range []ByteRange{{Offset: 0, Length: 1}, {Offset: 100, Length: 50}, {Offset: 900, Length: -1}, {Offset: 999, Length: 1}} {
		got, err := readAll(ctx, e.Store, key, &r)
		if err != nil {
			return fmt.Errorf("range %+v: %w", r, err)
		}
		end := int64(len(data))
		if r.Length >= 0 {
			end = r.Offset + r.Length
		}
		if !bytes.Equal(got, data[r.Offset:end]) {
			return fmt.Errorf("range %+v returned %d bytes that differ from the object's", r, len(got))
		}
	}
	return nil
}

// multipartPart is the smallest part S3 accepts but the last.
const multipartPart = 5 << 20

func checkMultipart(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("multipart")
	id, err := e.Store.CreateMultipart(ctx, key, "")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	e.mu.Lock()
	e.uploads = append(e.uploads, MultipartUpload{Key: key, UploadID: id})
	e.mu.Unlock()
	p1, p2 := payload(multipartPart), payload(1234)
	var parts []Part
	for i, d := range [][]byte{p1, p2} {
		p, err := e.Store.UploadPart(ctx, key, id, i+1, bytes.NewReader(d), int64(len(d)), Checksum{})
		if err != nil {
			return fmt.Errorf("part %d: %w", i+1, err)
		}
		p.Number = i + 1
		parts = append(parts, p)
	}
	if _, err := e.Store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		return wrongAnswer("Head of the object before CompleteMultipartUpload", err)
	}
	ups, err := e.Store.ListMultipart(ctx, key)
	if err != nil {
		return fmt.Errorf("list uploads: %w", err)
	}
	found := false
	for _, u := range ups {
		found = found || u.UploadID == id
	}
	if !found {
		return errors.New("the open upload is not listed by ListMultipartUploads")
	}
	if err := e.Store.CompleteMultipart(ctx, key, id, parts); err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	want := append(append([]byte{}, p1...), p2...)
	info, err := e.Store.Head(ctx, key)
	if err != nil {
		return fmt.Errorf("head after completion: %w", err)
	}
	if info.Size != int64(len(want)) {
		return fmt.Errorf("the completed object has %d bytes, want %d", info.Size, len(want))
	}
	got, err := readAll(ctx, e.Store, key, nil)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("the completed object differs from the parts that were sent")
	}
	return nil
}

func checkAbort(ctx context.Context, e *ConformanceEnv) error {
	key := e.Key("aborted")
	id, err := e.Store.CreateMultipart(ctx, key, "")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	d := payload(multipartPart)
	if _, err := e.Store.UploadPart(ctx, key, id, 1, bytes.NewReader(d), int64(len(d)), Checksum{}); err != nil {
		_ = e.Store.AbortMultipart(ctx, key, id)
		return fmt.Errorf("part: %w", err)
	}
	if err := e.Store.AbortMultipart(ctx, key, id); err != nil {
		return fmt.Errorf("abort: %w", err)
	}
	if _, err := e.Store.Head(ctx, key); !errors.Is(err, ErrNotFound) {
		return wrongAnswer("Head of the object after the upload was aborted", err)
	}
	ups, err := e.Store.ListMultipart(ctx, key)
	if err != nil {
		return fmt.Errorf("list uploads: %w", err)
	}
	for _, u := range ups {
		if u.UploadID == id {
			return errors.New("the aborted upload is still listed")
		}
	}
	return nil
}

func checkDeleteMany(ctx context.Context, e *ConformanceEnv) error {
	keys := []string{e.Key("many"), e.Key("many"), e.Key("many")}
	for _, k := range keys {
		if err := putBytes(ctx, e.Store, k, payload(10)); err != nil {
			return err
		}
	}
	failed, err := e.Store.DeleteMany(ctx, append(keys, e.Key("many-missing")))
	if err != nil {
		return fmt.Errorf("%w (a store without DeleteObjects needs storage.gc.batch_delete: false)", err)
	}
	if len(failed) > 0 {
		return fmt.Errorf("DeleteObjects did not delete %v", failed)
	}
	for _, k := range keys {
		if _, err := e.Store.Head(ctx, k); !errors.Is(err, ErrNotFound) {
			return wrongAnswer("Head of "+k+" after DeleteObjects", err)
		}
	}
	return nil
}

func checkPresign(ctx context.Context, e *ConformanceEnv) error {
	if e.Opts.HTTP == nil {
		return errors.New("no HTTP client was given to fetch the presigned URL")
	}
	key := e.Key("presigned")
	data := payload(2048)
	if err := putBytes(ctx, e.Store, key, data); err != nil {
		return err
	}
	u, err := e.Store.Presign(ctx, key, time.Minute)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := e.Opts.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fetch the presigned URL: %w", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, data) {
		return fmt.Errorf("the presigned URL answered %d with %d bytes", resp.StatusCode, len(got))
	}
	return nil
}

// StartupCheck is the check a replica passes before its first readiness
// (R-STOR-035): write, read back, compare and delete <prefix>/_probe/<replica>,
// then list <prefix>/sha256/ with one key. It makes one attempt; the error is a
// *StoreError for the store's answers.
func StartupCheck(ctx context.Context, s ObjectStore, prefix, replica string) error {
	if err := probe(ctx, s, prefix, replica); err != nil {
		return err
	}
	if _, err := s.List(ctx, KeyPrefix(prefix), ListOptions{Max: 1}); err != nil {
		return fmt.Errorf("list %s: %w", KeyPrefix(prefix), err)
	}
	return nil
}

type awaitOptions struct{ initial, max time.Duration }

// AwaitOption tunes AwaitStartupCheck.
type AwaitOption func(*awaitOptions)

// WithAwaitBackoff sets the first wait and the cap of the exponential backoff
// (500 ms and 30 s by default); tests use short values.
func WithAwaitBackoff(initial, max time.Duration) AwaitOption {
	return func(o *awaitOptions) { o.initial, o.max = initial, max }
}

// AwaitStartupCheck repeats StartupCheck until it passes. A configuration or
// permission error (AccessDenied, NoSuchBucket, an invalid region) ends it with
// that error, so the process stops with a clear message. A transient error
// (timeout, connection error, 5xx, throttling, an open circuit) is retried with
// exponential backoff capped at 30 s for as long as it lasts, logged once per
// minute; the replica stays not ready and does not exit, so a store outage does
// not turn into a crash loop.
func AwaitStartupCheck(ctx context.Context, s ObjectStore, prefix, replica string, log *slog.Logger, opts ...AwaitOption) error {
	o := awaitOptions{initial: 500 * time.Millisecond, max: 30 * time.Second}
	for _, f := range opts {
		f(&o)
	}
	backoff := o.initial
	var lastLog time.Time
	for {
		err := StartupCheck(ctx, s, prefix, replica)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !IsTransient(err) && !errors.Is(err, ErrCircuitOpen) {
			return err
		}
		if time.Since(lastLog) >= time.Minute {
			lastLog = time.Now()
			log.Warn("object-store startup check failed; retrying", "error", err, "retry_in", backoff.String())
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		backoff = min(backoff*2, o.max)
	}
}
