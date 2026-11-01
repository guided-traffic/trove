package nuget

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestR_NUGET_005_TheSearchTextIsBoundedAndValid: each part of the search document is cut to a
// bound at a character boundary and loses the bytes PostgreSQL's text type refuses, because a
// tsvector holds at most 1 MiB and a .nuspec may be that long (nuget.archive.max_nuspec_bytes):
// an unbounded description would fail the whole push.
func TestR_NUGET_005_TheSearchTextIsBoundedAndValid(t *testing.T) {
	long := strings.Repeat("ä", maxSearchText) // two bytes each: the cut falls inside a character
	got := cut(long)
	if len(got) > maxSearchText || !utf8.ValidString(got) || len(got) < maxSearchText-3 {
		t.Errorf("cut: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if got := cut("a\x00b\xffc"); got != "ab�c" {
		t.Errorf("cut = %q, want the NUL gone and the invalid byte replaced", got)
	}
	if got := cut("plain"); got != "plain" {
		t.Errorf("cut = %q", got)
	}
}

// TestR_NUGET_033_TheJournalEntryNamesTheNumberAndTheBytes: the entry of a published or deleted
// event is the repository, the lower-cased ID, the URL form of the version and the hex SHA-512,
// and nothing else (R-NUGET-033: IDs and states, never a secret).
func TestR_NUGET_033_TheJournalEntryNamesTheNumberAndTheBytes(t *testing.T) {
	raw, err := journalEntry(7, "acme.widgets", "1.2.3", []byte{0xab, 0xcd})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repository": 7.0, "id": "acme.widgets", "version": "1.2.3", "sha512": "abcd"}
	if len(m) != len(want) {
		t.Fatalf("entry = %s", raw)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
}

// TestR_NUGET_006_ANilActorIsTheSystemPrincipalOfTheTombstone: the tombstone column is a uuid
// and not null, so a removal nobody asked for records the nil UUID.
func TestR_NUGET_006_ANilActorIsTheSystemPrincipalOfTheTombstone(t *testing.T) {
	if got := actorID(""); got != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("actorID(\"\") = %q", got)
	}
	if got := actorID("abc"); got != "abc" {
		t.Errorf("actorID = %q", got)
	}
}
