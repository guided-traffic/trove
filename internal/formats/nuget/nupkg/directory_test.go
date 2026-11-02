package nupkg

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"hash/crc32"
	"io"
	"strings"
	"testing"
)

// withSecondDirectory returns the archive of bd with a second central directory and a second
// end record in the comment of the first end record, followed by two bytes. archive/zip takes the
// last end-record signature whose record fits before the end of the file, so it reads the second
// directory; a scan that takes the end record whose comment ends at the end of the file reads the
// first. replace gives entries of the second directory other bytes of the same length, written
// as local headers inside the comment; the entries have to be stored, so that the declared sizes
// of the two directories stay equal.
func withSecondDirectory(t testing.TB, bd build, replace map[string][]byte) []byte {
	t.Helper()
	plain := bd.bytes()
	end := plain[len(plain)-eocdLen:]
	cdSize, cdOff := int(le.Uint32(end[12:])), int(le.Uint32(end[16:]))
	cd := append([]byte(nil), plain[cdOff:cdOff+cdSize]...)
	base := len(plain) // the comment of the first end record starts here
	var comment bytes.Buffer
	for p := 0; p < len(cd); {
		nameLen, extraLen, commentLen := int(le.Uint16(cd[p+28:])), int(le.Uint16(cd[p+30:])), int(le.Uint16(cd[p+32:]))
		name := string(cd[p+centralLen : p+centralLen+nameLen])
		if body, ok := replace[name]; ok {
			if le.Uint16(cd[p+10:]) != 0 || int(le.Uint32(cd[p+24:])) != len(body) {
				t.Fatalf("%s: a replaced entry is stored and keeps its size", name)
			}
			sum := crc32.ChecksumIEEE(body)
			at := base + comment.Len()
			put32(&comment, sigLocal)
			put16(&comment, 20)
			put16(&comment, 0)
			put16(&comment, 0)
			put32(&comment, 0)
			put32(&comment, sum)
			put32(&comment, uint32(len(body)))
			put32(&comment, uint32(len(body)))
			put16(&comment, uint16(nameLen))
			put16(&comment, 0)
			comment.WriteString(name)
			comment.Write(body)
			le.PutUint32(cd[p+16:], sum)
			le.PutUint32(cd[p+42:], uint32(at))
		}
		p += centralLen + nameLen + extraLen + commentLen
	}
	cdAt := base + comment.Len()
	comment.Write(cd)
	n := le.Uint16(end[10:])
	put32(&comment, sigEOCD)
	put16(&comment, 0)
	put16(&comment, 0)
	put16(&comment, n)
	put16(&comment, n)
	put32(&comment, uint32(len(cd)))
	put32(&comment, uint32(cdAt))
	put16(&comment, 0)
	comment.WriteString("\x00\x00") // the second record's comment does not end at the end of the file
	bd.comment = comment.String()
	return bd.bytes()
}

// storedNuspec is good() with a stored .nuspec, whose bytes a second directory can replace.
func storedNuspec() build {
	bd := good()
	bd.ents = append([]ent(nil), bd.ents...)
	bd.ents[0].stored = true
	return bd
}

// TestR_NUGET_005_AnEndRecordAfterTheOneTheScanChoseIsRefused: the scan and archive/zip must read
// one central directory (F-26, Q-NUGET-89). archive/zip takes the last end-record signature whose
// record fits, also when bytes follow its comment; the scan takes the record whose comment ends at
// the end of the file. An archive with a second end record after the one the scan chose is
// refused, whether the second record names the same directory or one that points an entry at
// other bytes (here a .nuspec that declares another ID). A signature too close to the end to be a
// record is read by neither and stays in the comment.
func TestR_NUGET_005_AnEndRecordAfterTheOneTheScanChoseIsRefused(t *testing.T) {
	evil := []byte(strings.Replace(nuspecXML, "Acme.Widgets", "Evil.Widgets", 1))
	for _, c := range []struct {
		name    string
		data    []byte
		readsAs string // the ID archive/zip reads from the .nuspec of the second directory
	}{
		{"the same directory again", withSecondDirectory(t, storedNuspec(), nil), "Acme.Widgets"},
		{"a directory that points the .nuspec at other bytes", withSecondDirectory(t, storedNuspec(), map[string][]byte{"Acme.Widgets.nuspec": evil}), "Evil.Widgets"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The premise: archive/zip reads the second directory of this archive.
			zr, err := zip.NewReader(bytes.NewReader(c.data), int64(len(c.data)))
			if err != nil {
				t.Fatalf("archive/zip does not read the archive: %v", err)
			}
			rc, err := zr.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(rc)
			_ = rc.Close()
			if err != nil || !strings.Contains(string(body), "<id>"+c.readsAs+"</id>") {
				t.Fatalf("archive/zip reads %q (%v), want the .nuspec of %s", body, err, c.readsAs)
			}
			res, err := inspect(t, c.data, Limits{})
			if err == nil {
				t.Fatalf("accepted with the identity %q", res.Metadata.ID)
			}
			if !IsRefusal(err) || !errors.Is(err, ErrLocalMismatch) {
				t.Errorf("error = %v, want a refusal for %v", err, ErrLocalMismatch)
			}
		})
	}
	t.Run("a signature too close to the end to be a record", func(t *testing.T) {
		bd := storedNuspec()
		bd.comment = "PK\x05\x06" + strings.Repeat("\x00", eocdLen-5)
		res, err := inspect(t, bd.bytes(), Limits{})
		if err != nil || res.Metadata.ID != "Acme.Widgets" {
			t.Fatalf("refused or read otherwise: %v", err)
		}
	})
}

// TestR_NUGET_005_EveryEntryIsReadFromTheOffsetTheScanChecked: the entries archive/zip serves are
// held to the data offset the scan computed from the local header it checked, not only to name,
// flags, method and sizes (F-26, Q-NUGET-89). archive/zip shifts every offset by the bytes in front
// of an archive whose end record does not account for them; the directory of the same archive
// without those bytes agrees in everything else and is refused.
func TestR_NUGET_005_EveryEntryIsReadFromTheOffsetTheScanChecked(t *testing.T) {
	data := good().bytes()
	dir, err := scan(context.Background(), bytes.NewReader(data), int64(len(data)), Limits{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	same, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.readBy(same); err != nil {
		t.Fatalf("the archive and its own directory: %v", err)
	}
	prefixed := append([]byte("MZ\x90\x00"), data...)
	shifted, err := zip.NewReader(bytes.NewReader(prefixed), int64(len(prefixed)))
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range shifted.File {
		if f.Name != same.File[i].Name || f.UncompressedSize64 != same.File[i].UncompressedSize64 || f.CompressedSize64 != same.File[i].CompressedSize64 {
			t.Fatalf("entry %d differs in more than its offset", i)
		}
	}
	if err := dir.readBy(shifted); !errors.Is(err, ErrLocalMismatch) || !IsRefusal(err) {
		t.Errorf("entries read from other offsets: %v, want a refusal for %v", err, ErrLocalMismatch)
	}
}
