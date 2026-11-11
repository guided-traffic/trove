package ansible

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/ansible/ansibletest"
	"github.com/guided-traffic/trove/internal/formats/ansible/collection/collectiontest"
	"github.com/guided-traffic/trove/internal/formats/ansible/rows"
)

func docsSpec() collectiontest.Spec {
	s := collectionSpec("1.0.0")
	s.Files["plugins/modules/ping.py"] = "DOCUMENTATION = '''\nmodule: ping\nshort_description: Pings\n'''\n"
	s.Files["plugins/modules/check.py"] = "DOCUMENTATION = '''\nmodule: check\nshort_description: Checks\n'''\n"
	s.Files["plugins/filter/is_cidr.py"] = "DOCUMENTATION = '''\nname: is_cidr\nshort_description: CIDR\n'''\n"
	s.Files["plugins/module_utils/net.py"] = "x = 1\n"
	s.Files["docs/install.md"] = "# Install\n"
	return s
}

// TestR_ANS_033_APublishStoresTheGeneratedDocumentationWithTheVersion: the documentation of a published collection is put with the version
// (source generated, one entry per content item), and the removal of the version removes it.
func TestR_ANS_033_APublishStoresTheGeneratedDocumentationWithTheVersion(t *testing.T) {
	h := newHost(t)
	r, err := publish(h, collectiontest.Build(docsSpec()), true, "")
	if err != nil || r.Code != http.StatusAccepted {
		t.Fatalf("publish: %v %v", r, err)
	}
	st := h.FormatData(ID).(*ansibletest.Store)
	ctx := context.Background()
	repo := h.Ref().ID
	state, err := st.DocState(ctx, repo, "acme.tools", "1.0.0")
	if err != nil || state.State != rows.DocAvailable || state.Source != rows.DocSourceGenerated || state.Entries != 5 {
		t.Fatalf("state = %+v, %v", state, err)
	}
	set, _, _ := st.DocSet(ctx, repo, "acme.tools", "1.0.0")
	if set.Readme == nil || set.Readme.Body != "# tools 1.0.0\n" || len(set.Files) != 1 || set.Generator == 0 {
		t.Errorf("set = %+v", set)
	}
	if e, ok, _ := st.DocEntry(ctx, repo, "acme.tools", "1.0.0", "module", "ping"); !ok || !e.HasDocs || e.ShortDescription != "Pings" {
		t.Errorf("entry = %+v %v", e, ok)
	}
	rq, err := h.NewRequest(ctx, http.MethodGet, "v3/collections/acme/tools/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Remove(ctx, rq, "delete", ArtifactPath("acme", "tools", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.DocState(ctx, repo, "acme.tools", "1.0.0"); state.State != rows.DocNone {
		t.Errorf("after the removal: %+v", state)
	}
}

// TestR_ANS_034_APublishWhoseDocumentationHitsABoundIsAcceptedWithWarnings: the warnings of the generator are messages of level WARNING of
// the import task, at most 100 and one last line that counts the rest, and the publish is accepted.
func TestR_ANS_034_APublishWhoseDocumentationHitsABoundIsAcceptedWithWarnings(t *testing.T) {
	h := newHost(t)
	s := collectionSpec("1.0.0")
	s.Files["plugins/modules/open.py"] = "DOCUMENTATION = '''\nshort_description: x\n"
	for i := range 120 {
		s.Files[fmt.Sprintf("plugins/modules/bad__%03d.py", i)] = "x = 1\n"
	}
	r, err := publish(h, collectiontest.Build(s), true, "")
	if err != nil || r.Code != http.StatusAccepted {
		t.Fatalf("publish: %v %v", r, err)
	}
	task := taskOf(t, h, r)
	if task["state"] != "completed" {
		t.Fatalf("task = %v", task)
	}
	var warns int
	var last string
	for _, m := range task["messages"].([]any) {
		msg := m.(map[string]any)
		if msg["level"] == "WARNING" {
			warns++
			last, _ = msg["message"].(string)
		}
	}
	if warns != 101 || !strings.Contains(last, "more warnings are not listed") {
		t.Errorf("%d warnings, last %q", warns, last)
	}
}
