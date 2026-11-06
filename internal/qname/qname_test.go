package qname

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestR_REPO_002_ValidKey(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"npm", true}, {"npm-remote", true}, {"a1", true}, {"0a", true}, {"a.b_c-d", true}, {long, true},
		{"", false}, {"a", false}, {long + "a", false}, {"-ab", false}, {".ab", false}, {"_ab", false},
		{"Ab", false}, {"a b", false}, {"a/b", false}, {"a%2Fb", false}, {"a..b", false}, {"ab.", false},
		{"ab~cache", false}, {"äb", false},
	} {
		if got := ValidKey(tc.in); got != tc.want {
			t.Errorf("ValidKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestR_AUTHZ_026_ValidProjectKey(t *testing.T) {
	long := "a" + strings.Repeat("b", 31)
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"web", true}, {"web-app", true}, {"ab", true}, {"a1", true}, {long, true},
		{"", false}, {"w", false}, {long + "b", false}, {"1web", false}, {"Web", false}, {"web_x", false},
		{"web.x", false}, {"web/x", false}, {"-web", false},
	} {
		if got := ValidProjectKey(tc.in); got != tc.want {
			t.Errorf("ValidProjectKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestR_REPO_001_ValidFormat(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"npm", true}, {"nuget", true}, {"generic", true}, {"v1", false}, {"v10", false}, {"v1x", true},
		{"", false}, {"n", false}, {"Npm", false}, {"npm/x", false}, {"vv", true},
	} {
		if got := ValidFormat(tc.in); got != tc.want {
			t.Errorf("ValidFormat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestR_REPO_001_ParseRepo(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Repo
		wantErr error
	}{
		{"web/npm-remote", Repo{"web", "npm-remote"}, nil},
		{"web-app/a.b", Repo{"web-app", "a.b"}, nil},
		{"npm-remote", Repo{}, ErrNotQualified},
		{"", Repo{}, ErrNotQualified},
		{"web/npm/x", Repo{}, ErrNotQualified},
		{"web/", Repo{}, ErrInvalidKey},
		{"/npm", Repo{}, ErrInvalidProject},
		{"Web/npm", Repo{}, ErrInvalidProject},
		{"web/Npm", Repo{}, ErrInvalidKey},
		{"web/npm~cache", Repo{}, ErrInvalidKey},
		{"web/a..b", Repo{}, ErrInvalidKey},
		{"1web/npm", Repo{}, ErrInvalidProject},
	} {
		got, err := ParseRepo(tc.in)
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("ParseRepo(%q) = %+v, %v; want %+v, %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestR_REPO_001_RepoText(t *testing.T) {
	r := Repo{"web", "npm"}
	if r.String() != "web/npm" || (Repo{}).String() != "" || !(Repo{}).IsZero() || r.IsZero() {
		t.Fatalf("String/IsZero: %q", r.String())
	}
	b, err := json.Marshal(map[Repo]Repo{r: r})
	if err != nil || string(b) != `{"web/npm":"web/npm"}` {
		t.Fatalf("marshal = %s, %v", b, err)
	}
	var back map[Repo]Repo
	if err := json.Unmarshal(b, &back); err != nil || back[r] != r {
		t.Fatalf("unmarshal = %+v, %v", back, err)
	}
	if err := json.Unmarshal([]byte(`"npm"`), &r); err == nil {
		t.Error("an unqualified name unmarshalled")
	}
	if !r.Valid() || (Repo{"web", ""}).Valid() || (Repo{"", "npm"}).Valid() {
		t.Error("Valid")
	}
}

func TestR_AUTHZ_026_ParseObject(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Object
		wantErr error
	}{
		{"bot", Object{"", "bot"}, nil},
		{"web/bot", Object{"web", "bot"}, nil},
		{"", Object{}, ErrNotQualified},
		{"web/", Object{}, ErrNotQualified},
		{"/bot", Object{}, ErrNotQualified}, // an empty project part is no project key
		{"web/a/b", Object{}, ErrNotQualified},
		{"Web/bot", Object{}, ErrInvalidProject},
	} {
		got, err := ParseObject(tc.in)
		if tc.in == "/bot" {
			tc.wantErr = ErrInvalidProject
		}
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("ParseObject(%q) = %+v, %v; want %+v, %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
	if (Object{"web", "bot"}).String() != "web/bot" || (Object{"", "bot"}).String() != "bot" || !(Object{Name: "bot"}).InstanceWide() {
		t.Error("String/InstanceWide")
	}
	var o Object
	if err := json.Unmarshal([]byte(`"web/bot"`), &o); err != nil || o != (Object{"web", "bot"}) {
		t.Errorf("unmarshal = %+v, %v", o, err)
	}
	if b, _ := json.Marshal(o); string(b) != `"web/bot"` {
		t.Errorf("marshal = %s", b)
	}
}
