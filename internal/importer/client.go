package importer

import (
	"context"
	"crypto/sha1" //nolint:gosec // G505: the digest of an old registry's shasum, compared, never used to protect anything
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/outbound"
)

// Retry is the retry policy of a client (R-IMP-008): a transport failure, 429, 502, 503 and 504 are tried again with a
// growing wait that obeys Retry-After, up to Attempts requests in all.
type Retry struct {
	Attempts int           // requests per call, 5 by default
	Base     time.Duration // first wait, 500 ms by default; doubled per attempt
	MaxWait  time.Duration // longest single wait, also for a larger Retry-After; 2 minutes by default
	Sleep    func(ctx context.Context, d time.Duration) error
}

func (r Retry) attempts() int {
	if r.Attempts <= 0 {
		return 5
	}
	return r.Attempts
}

func (r Retry) wait(attempt int, h http.Header) time.Duration {
	base := r.Base
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	max := r.MaxWait
	if max <= 0 {
		max = 2 * time.Minute
	}
	d := base << attempt
	if ra := retryAfter(h); ra > 0 {
		d = ra
	}
	return min(d, max)
}

func (r Retry) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads Retry-After as delta seconds or an HTTP date (RFC 9110).
func retryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(max(n, 0)) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// Limiter spaces the requests to the source (R-IMP-008): at most Rate per second, and half the rate after the source said it
// is being asked too much. The zero Limiter and a nil one do not limit.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	slowest  time.Duration
	now      func() time.Time
}

// NewLimiter limits to perSecond requests per second; 0 or less means no limit.
func NewLimiter(perSecond float64) *Limiter {
	l := &Limiter{now: time.Now, slowest: 10 * time.Second}
	if perSecond > 0 {
		l.interval = time.Duration(float64(time.Second) / perSecond)
	}
	return l
}

// Wait blocks until the caller may send a request.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return ctx.Err()
	}
	l.mu.Lock()
	if l.interval <= 0 {
		l.mu.Unlock()
		return ctx.Err()
	}
	now := l.now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()
	if d := at.Sub(now); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return ctx.Err()
}

// Slow halves the rate, down to one request in ten seconds.
func (l *Limiter) Slow() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.interval <= 0 {
		l.interval = 500 * time.Millisecond
	} else {
		l.interval *= 2
	}
	l.interval = min(l.interval, l.slowest)
}

// Interval is the current spacing of requests, 0 without a limit.
func (l *Limiter) Interval() time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.interval
}

// Request is one request to the target.
type Request struct {
	Method string
	URL    string
	Header http.Header
	// Body returns the body and its length for one attempt; a retry calls it again.
	Body func() (io.Reader, int64, error)
}

// Response is an answer with its bounded body.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// maxAnswer bounds what is read of a target's answer.
const maxAnswer = 1 << 20

func sendWithRetry(ctx context.Context, hc *http.Client, rt Retry, lim *Limiter, build func(ctx context.Context) (*http.Request, error)) (*http.Response, error) {
	var last error
	for attempt := 0; attempt < rt.attempts(); attempt++ {
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		var h http.Header
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ue *url.Error
			if errors.As(err, &ue) {
				err = fmt.Errorf("%s %s: %w", ue.Op, redact(ue.URL), ue.Err)
			}
			if errors.Is(err, outbound.ErrEgressDenied) {
				return nil, err // the address policy will say the same to every attempt
			}
			last = err
		case retryable(resp.StatusCode):
			h = resp.Header
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				lim.Slow()
			}
			last = fmt.Errorf("%s answered %d", redact(req.URL.String()), resp.StatusCode)
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
			_ = resp.Body.Close()
		default:
			return resp, nil
		}
		if attempt == rt.attempts()-1 {
			break
		}
		if err := rt.sleep(ctx, rt.wait(attempt, h)); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// redact drops the query and the user information of an address for a message.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(address)"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

// SourceClient reads the registry that is copied from. Its credentials are decided by the hop policy of its HTTP client,
// which gives them to the origin only (R-IMP-002).
type SourceClient struct {
	HTTP    *http.Client
	Origin  *url.URL
	Extra   []string // host patterns that may serve files, asked without credentials
	Limiter *Limiter
	Retry   Retry
}

// Allowed reports whether an address may be fetched: the source's origin, or a host of the extra list.
func (c *SourceClient) Allowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if sameOrigin(c.Origin, u) {
		return true
	}
	host := strings.ToLower(u.Hostname())
	for _, pat := range c.Extra {
		if ok, _ := path.Match(strings.ToLower(pat), host); ok {
			return true
		}
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}

// ErrForeignHost is returned for an address outside the source's origin and extra hosts.
var ErrForeignHost = errors.New("the address is not on the source's origin or an allowed extra host")

// GetStatus fetches an address and returns its status and its body, read up to limit bytes; a longer body is an error.
func (c *SourceClient) GetStatus(ctx context.Context, raw, accept string, limit int64) (int, []byte, error) {
	if !c.Allowed(raw) {
		return 0, nil, ErrForeignHost
	}
	resp, err := sendWithRetry(ctx, c.HTTP, c.Retry, c.Limiter, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		return req, nil
	})
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body, limit)
	return resp.StatusCode, body, err
}

// GetJSON fetches an address and decodes a 200 answer; a 404 returns found false.
func (c *SourceClient) GetJSON(ctx context.Context, raw, accept string, limit int64, into any) (found bool, err error) {
	status, body, err := c.GetStatus(ctx, raw, accept, limit)
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return false, nil
	case status != http.StatusOK:
		return false, fmt.Errorf("%s answered %d", redact(raw), status)
	}
	body, err = maybeGunzip(body, limit)
	if err != nil {
		return false, fmt.Errorf("%s: %w", redact(raw), err)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return false, fmt.Errorf("%s: the answer is not the document of a registry: %w", redact(raw), err)
	}
	return true, nil
}

// ErrTooLarge is returned for a body over its limit.
var ErrTooLarge = errors.New("the body is larger than the limit")

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}

// newHashes returns a hash for each digest computed over every download: the ones a registry announces.
func newHashes() map[string]hash.Hash {
	return map[string]hash.Hash{"sha512": sha512.New(), "sha384": sha512.New384(), "sha256": sha256.New(), "sha1": sha1.New()} //nolint:gosec // G401: see the import
}

// Download fetches a file of the source into a spool file below dir, with its digests computed on the way. A file over limit
// bytes is refused.
func (c *SourceClient) Download(ctx context.Context, raw, dir string, limit int64) (*File, error) {
	if !c.Allowed(raw) {
		return nil, ErrForeignHost
	}
	resp, err := sendWithRetry(ctx, c.HTTP, c.Retry, c.Limiter, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", redact(raw), resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, ErrTooLarge
	}
	f, err := os.CreateTemp(dir, "trove-import-*")
	if err != nil {
		return nil, err
	}
	hs := newHashes()
	ws := []io.Writer{f}
	for _, h := range hs {
		ws = append(ws, h)
	}
	n, err := io.Copy(io.MultiWriter(ws...), io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = ErrTooLarge
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	out := &File{File: f, Size: n, Sums: Sums{}}
	for name, h := range hs {
		out.Sums[name] = h.Sum(nil)
	}
	return out, nil
}

// TargetClient writes the trove repository that is copied into. Its credentials are decided by the hop policy of its HTTP
// client.
type TargetClient struct {
	HTTP  *http.Client
	Base  *url.URL // the repository's address as a package manager is given it, with a trailing slash
	Retry Retry
}

// Do sends a request to an address of the target's origin and reads the answer, bounded.
func (c *TargetClient) Do(ctx context.Context, r Request) (*Response, error) {
	u, err := url.Parse(r.URL)
	if err != nil || !sameOrigin(c.Base, u) {
		return nil, fmt.Errorf("the address %s is not on the target", redact(r.URL))
	}
	resp, err := sendWithRetry(ctx, c.HTTP, c.Retry, nil, func(ctx context.Context) (*http.Request, error) {
		var body io.Reader
		var n int64
		if r.Body != nil {
			if body, n, err = r.Body(); err != nil {
				return nil, err
			}
		}
		req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
		if err != nil {
			return nil, err
		}
		if r.Body != nil {
			req.ContentLength = n
		}
		for k, vs := range r.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// refusal builds the refusal of an answer, with the code of the target's error body when it has one.
func refusal(resp *Response) *Refusal {
	r := &Refusal{Status: resp.Status}
	var b struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(resp.Body, &b) == nil {
		r.Code, r.Msg = b.Code, b.Message
		if r.Msg == "" {
			r.Msg = b.Error
		}
	}
	return r
}
