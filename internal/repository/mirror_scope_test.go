package repository

import (
	"reflect"
	"strings"
	"testing"
)

// scopeDoc validates a remote document of the format "mirrorable" with its include and exclude lists ("" for none).
func scopeDoc(t *testing.T, include, exclude, remote string) (Document, *validation) {
	t.Helper()
	body := `{"key":"m-remote","class":"remote","format":"mirrorable"`
	if include != "" {
		body += `,"includePatterns":` + include
	}
	if exclude != "" {
		body += `,"excludePatterns":` + exclude
	}
	body += `,"remote":` + remote + `}`
	d, err := DecodeDocument([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	_, v := validate(mirrorCatalog(), &d, true)
	return d, v
}

// restrictedScopeFields returns the violations of a Owner's update from the stored to the next document.
func restrictedScopeFields(t *testing.T, stored, next [3]string) map[string]string {
	t.Helper()
	cur, v := scopeDoc(t, stored[0], stored[1], stored[2])
	if err := v.err(); err != nil {
		t.Fatalf("stored: %v", err)
	}
	nd, v := scopeDoc(t, next[0], next[1], next[2])
	if err := v.err(); err != nil {
		t.Fatalf("next: %v", err)
	}
	restrictedRemote(v, Caller{Restricted: true}, &cur, &nd)
	out := map[string]string{}
	for k, c := range fieldCodes(v) {
		if k == "/includePatterns" || k == "/excludePatterns" || k == "/remote/url" || k == "/remote/extraHosts" {
			out[k] = c
		}
	}
	return out
}

// R-REPO-050, R-REPO-052, Q-REPO-142 (Q-REPO-193): a Owner keeps what an administrator chose for a mirror
// that stays a mirror: the patterns, the url and the extra hosts decide what the mirror stores and from where.
func TestR_REPO_050_RestrictedWriteKeepsTheScopeOfAMirror(t *testing.T) {
	const (
		remote = `{"url":"https://registry.example.org/","mode":"mirror"}`
		hosts  = `{"url":"https://registry.example.org/","mode":"mirror","extraHosts":[{"pattern":"cdn.example.org","sendCredentials":false}]}`
		other  = `{"url":"https://partner.example.test/","mode":"mirror"}`
		pull   = `{"url":"https://registry.example.org/","mode":"pullThrough"}`
		paused = `{"url":"https://registry.example.org/","mode":"mirror","mirror":{"paused":true}}`
		acme   = `["@acme/*"]`
	)
	var wide []string
	for c := 'a'; c <= 'z'; c++ {
		wide = append(wide, `"`+string(c)+`*"`)
	}
	widePatterns := "[" + strings.Join(wide, ",") + "]"
	restricted := func(pointers ...string) map[string]string {
		m := map[string]string{}
		for _, p := range pointers {
			m[p] = "mirror_restricted"
		}
		return m
	}
	for _, tc := range []struct {
		name         string
		stored, next [3]string
		want         map[string]string
	}{
		{"widening the include list is refused", [3]string{acme, "", remote}, [3]string{widePatterns, "", remote}, restricted("/includePatterns")},
		{"narrowing it is refused too", [3]string{`["@acme/*","@beta/*"]`, "", remote}, [3]string{acme, "", remote}, restricted("/includePatterns")},
		{"adding an exclude pattern is refused", [3]string{acme, "", remote}, [3]string{acme, `["@acme/internal-*"]`, remote}, restricted("/excludePatterns")},
		{"dropping an exclude pattern is refused", [3]string{acme, `["@acme/internal-*"]`, remote}, [3]string{acme, "", remote}, restricted("/excludePatterns")},
		{"changing the url is refused", [3]string{acme, "", remote}, [3]string{acme, "", other}, restricted("/remote/url")},
		{"adding an extra host is refused", [3]string{acme, "", remote}, [3]string{acme, "", hosts}, restricted("/remote/extraHosts")},
		{"the same lists in another order are kept", [3]string{`["@acme/*","@beta/*"]`, "", remote}, [3]string{`["@beta/*","@acme/*"]`, "", remote}, map[string]string{}},
		{"the stored mirror sent back is kept", [3]string{acme, "", hosts}, [3]string{acme, "", hosts}, map[string]string{}},
		{"pausing is allowed", [3]string{acme, "", remote}, [3]string{acme, "", paused}, map[string]string{}},
		{"a write that turns the mode back to pullThrough may change them", [3]string{acme, "", remote}, [3]string{widePatterns, "", pull}, map[string]string{}},
		{"a stored pullThrough remote keeps its freedom", [3]string{acme, "", pull}, [3]string{widePatterns, "", `{"url":"https://partner.example.test/"}`}, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := restrictedScopeFields(t, tc.stored, tc.next); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("violations = %v, want %v", got, tc.want)
			}
		})
	}
}

// R-REPO-052 (Q-REPO-194): a url with a query is not the well-known upstream for the bound rule, and a mirror refuses it,
// because the formats' requests drop the query; a remote in pullThrough keeps whatever it stored.
func TestR_REPO_052_AMirrorUrlCarriesNoQuery(t *testing.T) {
	for _, u := range []string{"https://registry.example.org/?", "https://registry.example.org/?x=1", "https://partner.example.test/?token=1"} {
		t.Run(u, func(t *testing.T) {
			_, v := scopeDoc(t, `["@acme/*"]`, "", `{"url":"`+u+`","mode":"mirror"}`)
			if got := fieldCodes(v)["/remote/url"]; got != "invalid_value" {
				t.Fatalf("/remote/url = %q (%v)", got, fieldCodes(v))
			}
		})
		t.Run("pullThrough "+u, func(t *testing.T) {
			_, v := scopeDoc(t, "", "", `{"url":"`+u+`"}`)
			if err := v.err(); err != nil {
				t.Fatalf("a stored pullThrough url is refused: %v", err)
			}
		})
	}
	// The well-known upstream written in other spellings is still the well-known upstream and needs its bound.
	for _, u := range []string{"HTTPS://Registry.Example.org:443", "https://registry.example.org"} {
		_, v := scopeDoc(t, "", "", `{"url":"`+u+`","mode":"mirror"}`)
		if got := fieldCodes(v)["/remote/mode"]; got != "mirror_unbounded" {
			t.Errorf("%s: /remote/mode = %q", u, got)
		}
	}
}
