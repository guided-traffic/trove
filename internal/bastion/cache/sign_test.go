package cache

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
	"time"
)

func testKey() []byte { return bytes.Repeat([]byte{7}, MinKeyBytes) }

func TestR_BAS_032_ASignedEntryVerifiesUnderItsKeyOnly(t *testing.T) {
	s, err := NewSigner(testKey())
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{
		Kind: EntryArtifact, SHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", Size: 12,
		Header: http.Header{"Etag": {`"9f86"`}}, Repository: "npm-all", Status: 200, ValidatedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
	}
	rec, err := s.SignEntry("/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz", e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.VerifyEntry("/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz", rec)
	if err != nil || got.SHA256 != e.SHA256 || got.Repository != "npm-all" || !got.ValidatedAt.Equal(e.ValidatedAt) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	// Moved to another cache key, signed by another key, altered by one byte: all refused.
	if _, err := s.VerifyEntry("/api/npm/npm-all/other", rec); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an entry under another cache key: %v", err)
	}
	other, _ := NewSigner(bytes.Repeat([]byte{8}, MinKeyBytes))
	if _, err := other.VerifyEntry("/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz", rec); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an entry under another key: %v", err)
	}
	tampered := bytes.Replace(rec, []byte("npm-all"), []byte("npm-evi"), 1)
	if _, err := s.VerifyEntry("/api/npm/npm-all/lodash/-/lodash-4.17.21.tgz", tampered); !errors.Is(err, ErrBadSignature) {
		t.Errorf("an altered entry: %v", err)
	}
	if _, err := s.Verify(DomainAnswer, "/x", s.Sign(DomainEntry, "/x", []byte("{}"))); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a record of one domain accepted as another: %v", err)
	}
	if _, err := s.VerifyEntry("/x", []byte("no separator")); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a record without a signature: %v", err)
	}
}

func TestR_BAS_032_AShortKeyIsRefused(t *testing.T) {
	if _, err := NewSigner([]byte("short")); err == nil {
		t.Fatal("a 5-byte key was accepted")
	}
}

func TestR_BAS_030_KeyHashIsTheSHA256OfTheCacheKey(t *testing.T) {
	if got := KeyHash(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("KeyHash(\"\") = %s", got)
	}
}
