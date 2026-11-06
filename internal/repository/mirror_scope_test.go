package repository

import (
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
