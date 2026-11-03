package iface_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/bastion/iface"
)

func TestR_BAS_013_SubjectHeaderRoundTrips(t *testing.T) {
	exp := time.Unix(1893456000, 0).UTC()
	cases := []struct {
		name string
		in   iface.Subject
		want string
	}{
		{"token", iface.Subject{Kind: iface.SubjectToken, TokenID: "0192-a", PrincipalID: "0191-b", ExpiresAt: &exp}, "token=0192-a; principal=0191-b; expires=1893456000"},
		{"token without expiry", iface.Subject{Kind: iface.SubjectToken, TokenID: "0192-a", PrincipalID: "0191-b"}, "token=0192-a; principal=0191-b"},
		{"anonymous", iface.Subject{Kind: iface.SubjectAnonymous}, "anonymous"},
		{"invalid", iface.Subject{Kind: iface.SubjectInvalid}, "invalid"},
		{"refused is written as invalid", iface.Subject{Kind: iface.SubjectAnonymousRefused}, "invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.Header()
			if got != c.want {
				t.Fatalf("Header() = %q, want %q", got, c.want)
			}
			back, err := iface.ParseSubjectHeader(got)
			if err != nil {
				t.Fatal(err)
			}
			if back.Header() != got {
				t.Fatalf("round trip %q -> %q", got, back.Header())
			}
		})
	}
}

func TestR_BAS_013_SubjectHeaderRefusesMalformedValues(t *testing.T) {
	for _, v := range []string{"", "token", "token=a", "token=a; principal=b; expires=x", "token=a; principal=b; extra=c", "token=a; token=b; principal=c", "other"} {
		if _, err := iface.ParseSubjectHeader(v); !errors.Is(err, iface.ErrMarking) {
			t.Errorf("ParseSubjectHeader(%q) = %v, want ErrMarking", v, err)
		}
	}
}

func TestR_BAS_013_MarkingIsSetParsedAndStripped(t *testing.T) {
	h := http.Header{}
	iface.Marking{
		Class: iface.ClassShared, Repository: "npm-all", Read: iface.ReadFull,
		Subject: iface.Subject{Kind: iface.SubjectAnonymous}, Interface: iface.InterfaceVersion,
	}.Set(h)
	m, ok, err := iface.ParseMarking(h)
	if err != nil || !ok {
		t.Fatalf("ParseMarking: ok=%v err=%v", ok, err)
	}
	if m.Class != iface.ClassShared || m.Repository != "npm-all" || m.Read != iface.ReadFull || m.Subject.Kind != iface.SubjectAnonymous || m.Interface != iface.InterfaceVersion {
		t.Fatalf("marking = %+v", m)
	}
	h.Set("Content-Type", "application/json")
	iface.StripMarking(h)
	if len(h) != 1 || h.Get("Content-Type") == "" {
		t.Fatalf("after StripMarking: %v", h)
	}
	if _, ok, _ := iface.ParseMarking(h); ok {
		t.Fatal("a stripped response is still marked")
	}
}

func TestR_BAS_013_ParseMarkingRefusesAnUnknownClassOrShape(t *testing.T) {
	h := http.Header{iface.HeaderClass: {"bogus"}}
	if _, ok, err := iface.ParseMarking(h); !ok || !errors.Is(err, iface.ErrMarking) {
		t.Fatalf("class: ok=%v err=%v", ok, err)
	}
	h = http.Header{iface.HeaderClass: {"pass"}, iface.HeaderRead: {"most"}}
	if _, _, err := iface.ParseMarking(h); !errors.Is(err, iface.ErrMarking) {
		t.Fatalf("read: %v", err)
	}
}

func TestR_BAS_015_CursorRoundTrips(t *testing.T) {
	at := time.Date(2026, 11, 2, 10, 0, 2, 900_000_000, time.UTC)
	entry := iface.Cursor{At: at, ID: "0192f2a1-0000-7000-8000-000000000001"}
	back, err := iface.ParseCursor(entry.String())
	if err != nil || back.ID != entry.ID || !back.At.Equal(at) || back.Synthetic() {
		t.Fatalf("entry cursor: %+v %v", back, err)
	}
	syn := iface.Cursor{At: at}
	if got := syn.String(); got != "t:2026-11-02T10:00:02.9Z" {
		t.Fatalf("synthetic form = %q", got)
	}
	back, err = iface.ParseCursor(syn.String())
	if err != nil || !back.Synthetic() || !back.At.Equal(at) {
		t.Fatalf("synthetic cursor: %+v %v", back, err)
	}
}

func TestR_BAS_015_CursorRefusesMalformedForms(t *testing.T) {
	for _, s := range []string{"", "t:", "t:yesterday", "!!!", "MjAyNi0xMS0wMlQxMDowMDowMFo"} {
		if _, err := iface.ParseCursor(s); !errors.Is(err, iface.ErrCursor) {
			t.Errorf("ParseCursor(%q) = %v, want ErrCursor", s, err)
		}
	}
}

func TestR_BAS_009_DecodeStrictRefusesWhatTheReaderDoesNotKnow(t *testing.T) {
	var a iface.CheckAnswer
	if err := iface.DecodeStrict(strings.NewReader(`{"subject":{"kind":"invalid"},"repositories":{}}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := iface.DecodeStrict(strings.NewReader(`{"subject":{"kind":"invalid"},"repositories":{},"extra":1}`), &a); err == nil {
		t.Fatal("an unknown member was accepted")
	}
	if err := iface.DecodeStrict(strings.NewReader(`{"subject":{"kind":"invalid"},"repositories":{}} {}`), &a); err == nil {
		t.Fatal("trailing data was accepted")
	}
}

func TestR_BAS_011_CheckRequestSendsAnExplicitNullCredential(t *testing.T) {
	b, err := iface.Marshal(iface.CheckRequest{ClientAddress: "203.0.113.7", Repositories: []string{"npm-all"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"credential":null`) {
		t.Fatalf("anonymous request = %s", b)
	}
}
