package blobtest_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
)

func TestMemStore(t *testing.T) {
	ctx := context.Background()
	t.Run("R-STOR-035 a multipart object is visible on completion only and parts below the minimum are refused", func(t *testing.T) {
		m := blobtest.New()
		id, err := m.CreateMultipart(ctx, "k", "")
		if err != nil {
			t.Fatal(err)
		}
		small := []byte("tiny")
		p1, err := m.UploadPart(ctx, "k", id, 1, bytes.NewReader(small), 4, blob.Checksum{})
		if err != nil {
			t.Fatal(err)
		}
		p1.Number = 1
		p2, _ := m.UploadPart(ctx, "k", id, 2, bytes.NewReader(small), 4, blob.Checksum{})
		p2.Number = 2
		if _, err := m.Head(ctx, "k"); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("visible before completion: %v", err)
		}
		if err := m.CompleteMultipart(ctx, "k", id, []blob.Part{p1, p2}); err == nil {
			t.Fatal("a 4-byte first part was accepted")
		}
		m.MinPartSize = 0
		if err := m.CompleteMultipart(ctx, "k", id, []blob.Part{p1, p2}); err != nil {
			t.Fatal(err)
		}
		if got, _ := m.Data("k"); string(got) != "tinytiny" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("R-STOR-006 a payload checksum that does not match fails the request", func(t *testing.T) {
		m := blobtest.New()
		data := []byte("hello")
		good := sha256.Sum256(data)
		if err := m.Put(ctx, "a", bytes.NewReader(data), 5, blob.Checksum{Algorithm: "sha256", Value: good[:]}); err != nil {
			t.Fatal(err)
		}
		bad := sha256.Sum256([]byte("other"))
		if err := m.Put(ctx, "b", bytes.NewReader(data), 5, blob.Checksum{Algorithm: "sha256", Value: bad[:]}); err == nil {
			t.Fatal("a wrong SHA-256 was accepted")
		}
	})
	t.Run("R-STOR-035 ranges, listing with a delimiter and deletion behave like the real store", func(t *testing.T) {
		m := blobtest.New()
		for _, k := range []string{"p/a/1", "p/a/2", "p/b/1", "p/c"} {
			if err := m.Put(ctx, k, bytes.NewReader([]byte("0123456789")), 10, blob.Checksum{}); err != nil {
				t.Fatal(err)
			}
		}
		body, _, err := m.Get(ctx, "p/c", &blob.ByteRange{Offset: 3, Length: 4})
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := io.ReadAll(body); string(got) != "3456" {
			t.Fatalf("got %q", got)
		}
		page, err := m.List(ctx, "p/", blob.ListOptions{Delimiter: "/"})
		if err != nil || len(page.Prefixes) != 2 || len(page.Objects) != 1 {
			t.Fatalf("%+v %v", page, err)
		}
		if _, err := m.DeleteMany(ctx, []string{"p/a/1", "p/a/2", "missing"}); err != nil {
			t.Fatal(err)
		}
		if k := m.Keys(""); len(k) != 2 {
			t.Fatalf("keys %v", k)
		}
	})
}
