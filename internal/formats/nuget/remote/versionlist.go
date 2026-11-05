package remote

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// VersionListBounded reads the version list of the flat container like [VersionList], but reads at most maxEntries versions and
// refuses a list that has more before it builds them: the entries are decoded one at a time, so a list of millions of tiny
// strings costs no more than maxEntries strings (R-ARCH-028, Q-NUGET-107). The coding of the stored bytes is undone within
// max bytes first (Decode).
func VersionListBounded(doc []byte, max int64, maxEntries int) ([]string, error) {
	doc, err := Decode(doc, max)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, invalid("The upstream's version list is not the document {\"versions\": [...]}.")
	}
	var list []string
	found := false
	for dec.More() {
		keyTok, err := dec.Token()
		key, _ := keyTok.(string)
		if err != nil {
			return nil, invalid("The upstream's version list is not the document {\"versions\": [...]}: " + shorten(err.Error()))
		}
		if key != "versions" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, invalid("The upstream's version list is not the document {\"versions\": [...]}: " + shorten(err.Error()))
			}
			continue
		}
		tok, err := dec.Token()
		if err != nil || tok != json.Delim('[') {
			return nil, invalid("The upstream's version list has no \"versions\" list.")
		}
		list, found = []string{}, true
		for dec.More() {
			if len(list) >= maxEntries {
				return nil, invalid("The upstream's version list has more than " + strconv.Itoa(maxEntries) + " versions; trove reads at most that many of one ID.")
			}
			var s string
			if err := dec.Decode(&s); err != nil {
				return nil, invalid("The upstream's version list has an entry that is no string: " + shorten(err.Error()))
			}
			list = append(list, s)
		}
		if _, err := dec.Token(); err != nil {
			return nil, invalid("The upstream's version list is not complete: " + shorten(err.Error()))
		}
	}
	if !found {
		return nil, invalid("The upstream's version list has no \"versions\" member.")
	}
	return list, nil
}
