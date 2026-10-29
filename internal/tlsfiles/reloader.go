package tlsfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Pair names a PEM certificate chain (leaf first) and its private key.
type Pair struct {
	CertFile string
	KeyFile  string
}

// Spec names the files of one set of material.
type Spec struct {
	// Pairs are served (a listener) or presented (a client). The first pair is
	// the default; a listener picks another one by SNI.
	Pairs []Pair
	// CAFiles are PEM bundles: the client CAs of a listener or the trusted roots
	// of a client.
	CAFiles []string
	// SystemRoots starts the pool from the system roots; the bundles are added.
	SystemRoots bool
}

// Labels identify the material in the metrics and the log: Kind is one of the
// Kind constants, Listener names the listener for KindListener.
type Labels struct {
	Kind     string
	Listener string
}

// Option configures a Reloader.
type Option func(*Reloader)

// WithLogger sets the logger of the reload records; the default discards them.
func WithLogger(l *slog.Logger) Option {
	return func(r *Reloader) {
		if l != nil {
			r.log = l
		}
	}
}

// WithMetrics records the expiry, reload and error families on m.
func WithMetrics(m *Metrics) Option { return func(r *Reloader) { r.m = m } }

// WithClock replaces time.Now, for the throttle of Refresh and the expiry check.
func WithClock(now func() time.Time) Option { return func(r *Reloader) { r.now = now } }

// Reloader holds one set of TLS material and replaces it when its files change.
// Its methods are safe for concurrent use; the handshake callbacks read an
// immutable snapshot and never touch a file.
type Reloader struct {
	labels   Labels
	log      *slog.Logger
	m        *Metrics
	now      func() time.Time
	hasPairs bool
	trusts   bool // CA files or system roots configured: Pool is never nil
	sysRoots *x509.CertPool
	sysErr   error

	mu       sync.Mutex // serialises passes; guards pieces and lastPass
	pieces   []*piece
	lastPass time.Time

	snap atomic.Pointer[snapshot]
	gen  atomic.Uint64
}

// snapshot is the material of one generation. It is never changed after it is
// published.
type snapshot struct {
	certs   []*tls.Certificate
	names   []map[string]struct{} // exact DNS names per certificate, lower case
	pool    *x509.CertPool
	missing error // why no certificate is served, when pairs are configured but none loaded
}

// piece is one pair or one bundle with its own load state.
type piece struct {
	pair   bool
	files  []string
	series series

	good    bool // content has loaded at least once
	goodSum [sha256.Size]byte
	badSum  [sha256.Size]byte
	badSet  bool // badSum is the content that failed last
	// retryErr is set while p.err is an error of the kind a later pass can see
	// again with the same content: a read error, or a certificate that is not valid
	// yet. Such an error is counted once per distinct text and does not stand for
	// the content, so the last bad content stays remembered across it (CR2-7).
	retryErr bool
	// emptySeen is set when a pass of a piece that has loaded before read an empty
	// file: a writer that truncates and then writes (an in-place rewrite) can be
	// read in between. The next pass decides; an empty file that stays empty is
	// refused and counted then.
	emptySeen bool
	err       error // problem of the last pass; nil when the current content is served

	cert  *tls.Certificate    // pair
	certs []*x509.Certificate // bundle
}

// New reads every file of spec once and returns the reloader. A file that does
// not load is reported by Loaded and Err, logged and counted; New itself never
// fails, so a listener can bind and pick the material up once it appears.
func New(spec Spec, labels Labels, opts ...Option) *Reloader {
	r := &Reloader{labels: labels, log: slog.New(slog.DiscardHandler), now: time.Now}
	for _, o := range opts {
		o(r)
	}
	for _, p := range spec.Pairs {
		r.pieces = append(r.pieces, &piece{
			pair: true, files: []string{p.CertFile, p.KeyFile},
			series: series{kind: labels.Kind, listener: labels.Listener, name: p.CertFile},
		})
	}
	for _, f := range spec.CAFiles {
		r.pieces = append(r.pieces, &piece{
			files:  []string{f},
			series: series{kind: labels.Kind, listener: labels.Listener, name: f},
		})
	}
	r.hasPairs = len(spec.Pairs) > 0
	r.trusts = spec.SystemRoots || len(spec.CAFiles) > 0
	if spec.SystemRoots {
		pool, err := x509.SystemCertPool()
		if err != nil {
			r.sysErr = fmt.Errorf("system roots: %w", err)
			pool = x509.NewCertPool()
		}
		r.sysRoots = pool
	}
	for _, p := range r.pieces {
		r.m.init(p.series)
	}
	r.mu.Lock()
	r.pass()
	r.publish()
	r.mu.Unlock()
	return r
}

// Reload reads every file again and swaps in what changed and loads. It returns
// the problems of this pass: nil means every piece serves its current files.
func (r *Reloader) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pass() {
		r.publish()
	}
	return r.errLocked()
}

// Refresh is Reload for callers that check on use (a new connection, a
// handshake): with maxAge 0 it always reads the files; otherwise it reads them
// only when the last pass is older than maxAge, and never waits for a pass that
// another goroutine is running.
func (r *Reloader) Refresh(maxAge time.Duration) {
	if maxAge <= 0 {
		_ = r.Reload()
		return
	}
	if !r.mu.TryLock() {
		return
	}
	defer r.mu.Unlock()
	if r.now().Sub(r.lastPass) < maxAge {
		return
	}
	if r.pass() {
		r.publish()
	}
}

// Run polls the files every interval until ctx ends (the listeners,
// server.tls.reload_interval). A non-positive interval returns at once.
func (r *Reloader) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = r.Reload()
		}
	}
}

// Loaded returns nil once every piece has loaded at least once. Until then the
// material is incomplete: a listener without a pair cannot serve, a client
// without its CA trusts nothing.
func (r *Reloader) Loaded() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	if r.sysErr != nil {
		errs = append(errs, r.sysErr)
	}
	for _, p := range r.pieces {
		if !p.good {
			errs = append(errs, p.err)
		}
	}
	return errors.Join(errs...)
}

// Err returns the problems of the last pass, including pieces that serve their
// previous content because the new one did not load.
func (r *Reloader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.errLocked()
}

func (r *Reloader) errLocked() error {
	var errs []error
	if r.sysErr != nil {
		errs = append(errs, r.sysErr)
	}
	for _, p := range r.pieces {
		if p.err != nil {
			errs = append(errs, p.err)
		}
	}
	return errors.Join(errs...)
}

// Generation increases with every swap. A caller that derives state from the
// material (a tls.Config, a transport) rebuilds it when the value changes.
// Read Generation before Pool or the certificates: they are then at least as
// new as the generation read.
func (r *Reloader) Generation() uint64 { return r.gen.Load() }

// Pool returns the trusted certificates. It is nil only when neither CA files
// nor the system roots are configured; a configured bundle that never loaded
// gives an empty pool, which trusts nothing (a nil RootCAs would mean the system
// roots).
func (r *Reloader) Pool() *x509.CertPool { return r.snap.Load().pool }

// Leaves returns the leaf certificates served, in configuration order, without
// the pairs that never loaded.
func (r *Reloader) Leaves() []*x509.Certificate {
	s := r.snap.Load()
	out := make([]*x509.Certificate, len(s.certs))
	for i, c := range s.certs {
		out[i] = c.Leaf
	}
	return out
}

// Certificate is tls.Config.GetCertificate: the pair whose names hold the SNI
// name exactly, else the first pair that matches it (a wildcard) and that the
// client can use, else the first pair (the default).
func (r *Reloader) Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s := r.snap.Load()
	if len(s.certs) == 0 {
		return nil, s.missing
	}
	if hello == nil || len(s.certs) == 1 {
		return s.certs[0], nil
	}
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	if name == "" {
		return s.certs[0], nil
	}
	for i, c := range s.certs {
		if _, ok := s.names[i][name]; ok && hello.SupportsCertificate(c) == nil {
			return c, nil
		}
	}
	for _, c := range s.certs {
		if hello.SupportsCertificate(c) == nil {
			return c, nil
		}
	}
	return s.certs[0], nil
}

// ClientCertificate is tls.Config.GetClientCertificate. Without a configured
// pair it presents none; with a pair that never loaded it fails the handshake
// rather than connect without the certificate.
func (r *Reloader) ClientCertificate(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	s := r.snap.Load()
	if len(s.certs) == 0 {
		if r.hasPairs {
			return nil, s.missing
		}
		return &tls.Certificate{}, nil
	}
	if req != nil && len(s.certs) > 1 {
		for _, c := range s.certs {
			if req.SupportsCertificate(c) == nil {
				return c, nil
			}
		}
	}
	return s.certs[0], nil
}

// pass reads every piece once. It reports whether a piece swapped.
func (r *Reloader) pass() bool {
	changed := false
	for _, p := range r.pieces {
		if r.passPiece(p) {
			changed = true
		}
	}
	r.lastPass = r.now()
	return changed
}

func (r *Reloader) passPiece(p *piece) bool {
	parts, err := p.read()
	if err != nil {
		r.fail(p, err, nil)
		return false
	}
	if p.good && hasEmpty(parts) && !p.emptySeen {
		p.emptySeen = true
		return false // a rewrite in progress, most likely; the next pass reads it again
	}
	p.emptySeen = false
	sum := digest(parts)
	if p.good && sum == p.goodSum {
		p.err, p.badSet = nil, false
		return false
	}
	if p.badSet && sum == p.badSum {
		return false // the content that failed before; counted and logged then
	}
	loaded, err := r.parse(p, parts)
	if err != nil && p.pair {
		// A pair is two files. A rotation that rewrites them one after the other
		// can be read half-way; read once more before refusing the content.
		if again, rerr := p.read(); rerr == nil {
			if s2 := digest(again); s2 != sum {
				sum, parts = s2, again
				if p.good && sum == p.goodSum {
					p.err, p.badSet = nil, false
					return false
				}
				loaded, err = r.parse(p, parts)
			}
		}
	}
	if errors.Is(err, errNotYetValid) {
		// Not remembered by its content: the same bytes load once their time has come.
		r.fail(p, err, nil)
		return false
	}
	if err != nil {
		r.fail(p, err, &sum)
		return false
	}
	r.swap(p, loaded, sum)
	return true
}

func (p *piece) read() ([][]byte, error) {
	out := make([][]byte, len(p.files))
	for i, f := range p.files {
		b, err := os.ReadFile(f) //nolint:gosec // G304: operator-configured TLS file
		if err != nil {
			return nil, err // the error names the file
		}
		out[i] = b
	}
	return out, nil
}

// hasEmpty reports whether one of the files read was empty.
func hasEmpty(parts [][]byte) bool {
	for _, b := range parts {
		if len(b) == 0 {
			return true
		}
	}
	return false
}

// digest hashes the parts with their lengths, so moving bytes from one file to
// the other changes it.
func digest(parts [][]byte) [sha256.Size]byte {
	h := sha256.New()
	var n [8]byte
	for _, b := range parts {
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// loaded is the parsed content of a piece.
type loaded struct {
	cert  *tls.Certificate
	certs []*x509.Certificate
}

func (r *Reloader) parse(p *piece, parts [][]byte) (loaded, error) {
	if p.pair {
		cert, err := r.parsePair(parts[0], parts[1])
		if err != nil {
			return loaded{}, fmt.Errorf("certificate %s with key %s: %w", p.files[0], p.files[1], err)
		}
		return loaded{cert: cert}, nil
	}
	certs, err := parseBundle(parts[0])
	if err != nil {
		return loaded{}, fmt.Errorf("CA bundle %s: %w", p.files[0], err)
	}
	return loaded{certs: certs}, nil
}

// errNotYetValid is the error of a certificate whose NotBefore lies ahead.
var errNotYetValid = errors.New("the certificate is not valid")

func (r *Reloader) parsePair(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	if err := complete(certPEM); err != nil {
		return nil, fmt.Errorf("certificate file: %w", err)
	}
	if err := complete(keyPEM); err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, err
		}
	}
	now := r.now()
	if now.After(cert.Leaf.NotAfter) {
		return nil, fmt.Errorf("the certificate expired at %s", cert.Leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if now.Before(cert.Leaf.NotBefore) {
		// A pair rotated in early would break every handshake until its time comes:
		// the old one stays, and the same content is looked at again on every pass.
		return nil, fmt.Errorf("%w until %s", errNotYetValid, cert.Leaf.NotBefore.UTC().Format(time.RFC3339))
	}
	return &cert, nil
}

// complete refuses a file whose last PEM block is cut off: a reader that
// catches a file half-written would otherwise accept a shorter chain or bundle.
func complete(data []byte) error {
	rest := data
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
	}
	if bytes.Contains(rest, []byte("-----BEGIN")) {
		return errors.New("a PEM block is incomplete (file written half-way?)")
	}
	return nil
}

func parseBundle(data []byte) ([]*x509.Certificate, error) {
	if err := complete(data); err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("holds no PEM certificate")
	}
	return out, nil
}

func (r *Reloader) attrs(p *piece) []any {
	a := []any{"kind", r.labels.Kind}
	if r.labels.Listener != "" {
		a = append(a, "listener", r.labels.Listener)
	}
	if p.pair {
		return append(a, "cert_file", p.files[0], "key_file", p.files[1])
	}
	return append(a, "ca_file", p.files[0])
}

// fail records a piece that did not load. One distinct content (or one distinct
// read error) is counted and logged once, however many passes see it.
func (r *Reloader) fail(p *piece, err error, sum *[sha256.Size]byte) {
	repeat := sum == nil && p.retryErr && p.err != nil && p.err.Error() == err.Error()
	if sum != nil {
		p.badSum, p.badSet = *sum, true
	}
	p.retryErr = sum == nil
	p.err = err
	if repeat {
		return
	}
	r.m.failed(p.series)
	if p.good {
		r.log.Error("TLS material did not load; the previous material stays in service", append(r.attrs(p), "error", err.Error())...)
		return
	}
	r.log.Error("TLS material did not load; nothing is served from it until it does", append(r.attrs(p), "error", err.Error())...)
}

func (r *Reloader) swap(p *piece, l loaded, sum [sha256.Size]byte) {
	rotated := p.good
	p.cert, p.certs = l.cert, l.certs
	p.good, p.goodSum = true, sum
	p.err, p.badSet = nil, false
	notAfter := p.notAfter()
	r.m.setExpiry(p.series, float64(notAfter.Unix()))
	if !rotated {
		return
	}
	r.m.reloaded(p.series)
	a := append(r.attrs(p), "not_after", notAfter.UTC().Format(time.RFC3339))
	if p.pair {
		a = append(a, "serial", p.cert.Leaf.SerialNumber.Text(16), "subject", p.cert.Leaf.Subject.String())
	} else {
		a = append(a, "certificates", len(p.certs))
	}
	r.log.Info("TLS material reloaded", a...)
}

// notAfter is the leaf's expiry for a pair, the earliest for a bundle.
func (p *piece) notAfter() time.Time {
	if p.pair {
		return p.cert.Leaf.NotAfter
	}
	var first time.Time
	for i, c := range p.certs {
		if i == 0 || c.NotAfter.Before(first) {
			first = c.NotAfter
		}
	}
	return first
}

// publish builds the snapshot of the pieces that loaded.
func (r *Reloader) publish() {
	s := &snapshot{}
	var missing []error
	for _, p := range r.pieces {
		switch {
		case !p.good:
			if p.pair {
				missing = append(missing, p.err)
			}
		case p.pair:
			s.certs = append(s.certs, p.cert)
			names := map[string]struct{}{}
			for _, n := range p.cert.Leaf.DNSNames {
				names[strings.ToLower(strings.TrimSuffix(n, "."))] = struct{}{}
			}
			s.names = append(s.names, names)
		}
	}
	if r.trusts {
		if r.sysRoots != nil {
			s.pool = r.sysRoots.Clone()
		} else {
			s.pool = x509.NewCertPool()
		}
		for _, p := range r.pieces {
			if !p.pair && p.good {
				for _, c := range p.certs {
					s.pool.AddCert(c)
				}
			}
		}
	}
	if len(s.certs) == 0 && r.hasPairs {
		s.missing = fmt.Errorf("no TLS certificate loaded: %w", errors.Join(missing...))
	}
	r.snap.Store(s)
	r.gen.Add(1)
}
