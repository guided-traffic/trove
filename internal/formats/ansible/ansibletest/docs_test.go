package ansibletest_test

import (
	"context"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/ansible"
	"github.com/guided-traffic/trove/internal/formats/ansible/ansibletest"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

var _ ansible.Store = (*ansibletest.Store)(nil)

func docSet() rows.DocSet {
	return rows.DocSet{
		Source: rows.DocSourceGenerated, Generator: 1,
		Readme: &rows.DocFile{Name: "README.md", Format: rows.DocFormatMarkdown, Body: "# tools\n"},
		Files:  []rows.DocFile{{Name: "install.md", Format: rows.DocFormatMarkdown, Body: "# Install\n"}},
		Entries: []rows.DocEntry{
			{Type: "module", Name: "check_host_reachable", ShortDescription: "Checks whether a host answers", HasDocs: true, SearchText: "check host reachable Checks whether a host answers"},
			{Type: "filter", Name: "is_cidr", SearchText: "is cidr"},
			{Type: "module", Name: "ping", ShortDescription: "Pings", HasDocs: true, SearchText: "ping Pings"},
		},
	}
}

// TestR_ANS_033_TheTwinReportsTheStatesOfTheDocumentation: a version has the state available once its rows are put, pending while a fetch
// is queued, none when the queued row is older than an hour, unavailable with the reason of a failed fetch, and none again after the
// rows are removed.
func TestR_ANS_033_TheTwinReportsTheStatesOfTheDocumentation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	s := &ansibletest.Store{Now: func() time.Time { return now }}
	state := func() rows.DocState {
		t.Helper()
		st, err := s.DocState(ctx, 1, "acme.tools", "1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if got := state().State; got != rows.DocNone {
		t.Fatalf("an empty store: %s", got)
	}
	if err := s.EnqueueDocsFill(ctx, nil, 1, "acme.tools", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDocsFill(ctx, nil, 1, "acme.tools", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := state().State; got != rows.DocPending || len(s.DocsJobs) != 1 {
		t.Errorf("queued: state %s, %d jobs, want pending and one job however often it is asked", got, len(s.DocsJobs))
	}
	now = now.Add(61 * time.Minute)
	if got := state().State; got != rows.DocNone {
		t.Errorf("a queued row older than an hour: %s, want none", got)
	}
	if err := s.SetDocFetch(ctx, nil, 1, "acme.tools", "1.0.0", rows.DocFetchUnavailable, rows.DocReasonTooLarge); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.State != rows.DocUnavailable || st.Reason != rows.DocReasonTooLarge {
		t.Errorf("unavailable: %+v", st)
	}
	if err := s.PutDocs(ctx, nil, 1, "acme.tools", "1.0.0", docSet()); err != nil {
		t.Fatal(err)
	}
	if st := state(); st.State != rows.DocAvailable || st.Source != rows.DocSourceGenerated || st.Entries != 3 || st.Reason != "" {
		t.Errorf("available: %+v", st)
	}
	if err := s.RemoveDocs(ctx, nil, 1, "acme.tools", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := state().State; got != rows.DocNone {
		t.Errorf("after the removal: %s", got)
	}
	if err := s.RemoveDocs(ctx, nil, 1, "acme.tools", "9.9.9"); err != nil {
		t.Errorf("removing a missing version: %v", err)
	}
}

// TestR_ANS_036_TheTwinsTreeKeepsTheCountsWhileASearchFilters: the entries come in type and name order, a term matches the name as a
// substring or a word of the search text as a prefix, every term must match, and the counts are those before the search.
func TestR_ANS_036_TheTwinsTreeKeepsTheCountsWhileASearchFilters(t *testing.T) {
	ctx := context.Background()
	s := &ansibletest.Store{}
	if err := s.PutDocs(ctx, nil, 1, "acme.tools", "1.0.0", docSet()); err != nil {
		t.Fatal(err)
	}
	tree, err := s.DocTree(ctx, 1, "acme.tools", "1.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tree.CountBeforeQuery != 3 || len(tree.Entries) != 3 || tree.Entries[0].Type != "filter" || tree.Entries[1].Name != "check_host_reachable" {
		t.Errorf("tree = %+v", tree)
	}
	if tree.Counts["module"] != 2 || tree.Counts["filter"] != 1 || len(tree.Files) != 2 || tree.Files[0].Title != "tools" || tree.Files[1].Name != "install.md" {
		t.Errorf("counts %v, files %+v", tree.Counts, tree.Files)
	}
	for name, tc := range map[string]struct {
		terms []string
		want  []string
	}{
		"a name substring":   {[]string{"reach"}, []string{"check_host_reachable"}},
		"a word prefix":      {[]string{"answ"}, []string{"check_host_reachable"}},
		"every term":         {[]string{"host", "ping"}, nil},
		"case is ignored":    {[]string{"PING"}, []string{"ping"}},
		"a file title match": {[]string{"install"}, nil},
	} {
		got, err := s.DocTree(ctx, 1, "acme.tools", "1.0.0", &rows.DocQuery{Terms: tc.terms})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range got.Entries {
			names = append(names, e.Name)
		}
		if len(names) != len(tc.want) || (len(names) > 0 && names[0] != tc.want[0]) {
			t.Errorf("%s: entries %v, want %v", name, names, tc.want)
		}
		if got.CountBeforeQuery != 3 || got.Counts["module"] != 2 {
			t.Errorf("%s: the counts changed with the search: %d %v", name, got.CountBeforeQuery, got.Counts)
		}
	}
	if got, _ := s.DocTree(ctx, 1, "acme.tools", "1.0.0", &rows.DocQuery{Terms: []string{"install"}}); len(got.Files) != 1 || got.Files[0].Name != "install.md" {
		t.Errorf("a file matches by its title: %+v", got.Files)
	}
	if e, ok, _ := s.DocEntry(ctx, 1, "acme.tools", "1.0.0", "module", "ping"); !ok || e.Name != "ping" {
		t.Errorf("entry: %+v %v", e, ok)
	}
	if _, ok, _ := s.DocEntry(ctx, 1, "acme.tools", "1.0.0", "module", "nope"); ok {
		t.Error("an entry that does not exist was found")
	}
	if f, ok, _ := s.DocFile(ctx, 1, "acme.tools", "1.0.0", "README.md"); !ok || f.Body != "# tools\n" {
		t.Errorf("file: %+v %v", f, ok)
	}
	if _, ok, _ := s.DocFile(ctx, 1, "acme.tools", "1.0.0", "other.md"); ok {
		t.Error("a file that is not listed was found")
	}
	if set, ok, _ := s.DocSet(ctx, 1, "acme.tools", "1.0.0"); !ok || len(set.Entries) != 3 {
		t.Errorf("set: %v %d", ok, len(set.Entries))
	}
	if _, ok, _ := s.DocSet(ctx, 2, "acme.tools", "1.0.0"); ok {
		t.Error("the rows of another repository were found")
	}
}
