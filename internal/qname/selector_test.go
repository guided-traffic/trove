package qname

import (
	"errors"
	"strings"
	"testing"
)

var (
	instance = Context{}
	inWeb    = Context{Project: "web"}
)

func TestR_AUTHZ_004_ParseSelector(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		ctx     Context
		want    Selector
		wantErr error
	}{
		// Instance context.
		{"key", "web/npm-internal", instance, Selector{Kind: SelectorKey, Project: "web", Key: "npm-internal"}, nil},
		{"pattern", "web/npm-*", instance, Selector{Kind: SelectorPattern, ProjectGlob: "web", KeyGlob: "npm-*"}, nil},
		{"pattern with project glob and format", "w*/npm-*:npm", instance, Selector{Kind: SelectorPattern, ProjectGlob: "w*", KeyGlob: "npm-*", Format: "npm"}, nil},
		{"all projects pattern", "*/npm-*", instance, Selector{Kind: SelectorPattern, ProjectGlob: "*", KeyGlob: "npm-*"}, nil},
		{"question mark", "web/a?c", instance, Selector{Kind: SelectorPattern, ProjectGlob: "web", KeyGlob: "a?c"}, nil},
		{"class any", "@any", instance, Selector{Kind: SelectorClass, Class: "any"}, nil},
		{"class local with format", "@any-local:npm", instance, Selector{Kind: SelectorClass, Class: "local", Format: "npm"}, nil},
		{"class remote", "@any-remote", instance, Selector{Kind: SelectorClass, Class: "remote"}, nil},
		{"class virtual", "@any-virtual:nuget", instance, Selector{Kind: SelectorClass, Class: "virtual", Format: "nuget"}, nil},
		{"bare key", "npm-internal", instance, Selector{}, ErrBareKeyInInstanceContext},
		{"bare pattern", "npm-*", instance, Selector{}, ErrBareKeyInInstanceContext},
		{"bare pattern with format", "npm-*:npm", instance, Selector{}, ErrBareKeyInInstanceContext},
		{"three segments", "web/npm/x", instance, Selector{}, ErrInvalidSelector},
		{"invalid project", "Web/npm-internal", instance, Selector{}, ErrInvalidSelector},
		{"empty project", "/npm-internal", instance, Selector{}, ErrInvalidSelector},
		{"empty key", "web/", instance, Selector{}, ErrInvalidSelector},
		{"empty key glob", "web/*", instance, Selector{Kind: SelectorPattern, ProjectGlob: "web", KeyGlob: "*"}, nil},
		{"empty glob side", "*/", instance, Selector{}, ErrInvalidSelector},
		{"dot in project glob", "w.*/npm", instance, Selector{}, ErrInvalidSelector},
		{"slash glob does not cross", "web/*/x", instance, Selector{}, ErrInvalidSelector},
		{"key with upper case", "web/Npm", instance, Selector{}, ErrInvalidSelector},
		{"format on a key", "web/npm-internal:npm", instance, Selector{}, ErrInvalidSelector},
		{"empty format", "web/npm-*:", instance, Selector{}, ErrInvalidSelector},
		{"bad format", "web/npm-*:Npm", instance, Selector{}, ErrInvalidSelector},
		{"unknown class", "@any-other", instance, Selector{}, ErrInvalidSelector},
		{"class with a project", "web/@any", instance, Selector{}, ErrInvalidSelector},
		{"empty", "", instance, Selector{}, ErrInvalidSelector},
		{"too long", "web/" + strings.Repeat("a", MaxSelectorLength), instance, Selector{}, ErrInvalidSelector},

		// Project context.
		{"project key", "npm-internal", inWeb, Selector{Kind: SelectorKey, Project: "web", Key: "npm-internal"}, nil},
		{"project pattern", "npm-*", inWeb, Selector{Kind: SelectorPattern, Project: "web", KeyGlob: "npm-*"}, nil},
		{"project pattern with format", "npm-*:npm", inWeb, Selector{Kind: SelectorPattern, Project: "web", KeyGlob: "npm-*", Format: "npm"}, nil},
		{"project class", "@any-local:npm", inWeb, Selector{Kind: SelectorClass, Project: "web", Class: "local", Format: "npm"}, nil},
		{"project any", "@any", inWeb, Selector{Kind: SelectorClass, Project: "web", Class: "any"}, nil},
		{"qualified key in a project", "ops/npm-internal", inWeb, Selector{}, ErrQualifiedInProject},
		{"own project qualified", "web/npm-internal", inWeb, Selector{}, ErrQualifiedInProject},
		{"qualified pattern in a project", "w*/npm-*", inWeb, Selector{}, ErrQualifiedInProject},
		{"project invalid key", "n", inWeb, Selector{}, ErrInvalidSelector},
		{"project format on a key", "npm-internal:npm", inWeb, Selector{}, ErrInvalidSelector},
		{"project pattern with a dot", "npm.*", inWeb, Selector{Kind: SelectorPattern, Project: "web", KeyGlob: "npm.*"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSelector(tc.in, tc.ctx)
			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("ParseSelector(%q, %+v) = %+v, %v; want %+v, %v", tc.in, tc.ctx, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestR_AUTHZ_004_SelectorTextRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		in  string
		ctx Context
	}{
		{"web/npm-internal", instance},
		{"w*/npm-*:npm", instance},
		{"@any", instance},
		{"@any-remote:npm", instance},
		{"npm-internal", inWeb},
		{"npm-*:npm", inWeb},
		{"@any-virtual", inWeb},
	} {
		s, err := ParseSelector(tc.in, tc.ctx)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got := s.Text(tc.ctx); got != tc.in {
			t.Errorf("Text = %q, want %q", got, tc.in)
		}
	}
}

func TestR_AUTHZ_004_Glob(t *testing.T) {
	for _, tc := range []struct {
		glob, name string
		want       bool
	}{
		{"npm-*", "npm-remote", true},
		{"npm-*", "npm-", true},
		{"npm-*", "npm", false},
		{"*", "anything", true},
		{"*", "", true},
		{"", "", true},
		{"", "a", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"a?c", "abbc", false},
		{"*-remote", "npm-remote", true},
		{"*-remote", "npm-remotes", false},
		{"a*b*c", "aXXbYYc", true},
		{"a*b*c", "aXXcYYb", false},
		{"**", "x", true},
		{"a**b", "aXb", true},
		{"w*", "web", true},
		{"w*", "ops", false},
		{"*a*a*a*a*b", strings.Repeat("a", 40), false},
	} {
		if got := Glob(tc.glob, tc.name); got != tc.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", tc.glob, tc.name, got, tc.want)
		}
	}
}
