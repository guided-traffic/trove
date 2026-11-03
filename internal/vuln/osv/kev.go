package osv

import (
	"fmt"
	"regexp"
	"time"

	"github.com/guided-traffic/trove/internal/formats/untrusted"
)

// The bounds of the catalog (R-VULN-008).
const (
	// MaxKEVEntries and MaxKEVBytes bound the catalog.
	MaxKEVEntries = 100_000
	MaxKEVBytes   = 16 << 20
)

var cvePattern = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)

// KEVEntry is one entry of the CISA known-exploited catalog.
type KEVEntry struct {
	CVE       string
	DateAdded time.Time
	// DueDate is the zero time when the catalog gives none.
	DueDate time.Time
	// Ransomware is knownRansomwareCampaignUse.
	Ransomware string
}

type wireKEV struct {
	Vulnerabilities *[]struct {
		CVEID                      string `json:"cveID"`
		DateAdded                  string `json:"dateAdded"`
		DueDate                    string `json:"dueDate"`
		KnownRansomwareCampaignUse string `json:"knownRansomwareCampaignUse"`
	} `json:"vulnerabilities"`
}

const kevDate = "2006-01-02"

// DecodeKEV validates and decodes the catalog (R-VULN-008): at most 16 MiB, a top-level `vulnerabilities` array of at most
// 100,000 entries, each with a `cveID` of the form CVE-<year>-<4 or more digits> and a `dateAdded` date. A catalog that fails
// a rule is refused as a whole: it is a document of a source that is well-formed, so a violation says the source is damaged.
// An identifier listed twice keeps the earliest date.
func DecodeKEV(data []byte) ([]KEVEntry, error) {
	if len(data) > MaxKEVBytes {
		return nil, fmt.Errorf("the catalog is larger than %d bytes", MaxKEVBytes)
	}
	var w wireKEV
	l := untrusted.DefaultLimits()
	l.JSONMembers = 4 * MaxKEVEntries * 16
	if err := untrusted.UnmarshalJSON(data, l, &w); err != nil {
		return nil, fmt.Errorf("the catalog is not valid JSON of the expected shape: %w", err)
	}
	if w.Vulnerabilities == nil {
		return nil, fmt.Errorf("the catalog has no vulnerabilities array")
	}
	if len(*w.Vulnerabilities) > MaxKEVEntries {
		return nil, fmt.Errorf("the catalog lists more than %d entries", MaxKEVEntries)
	}
	byID := make(map[string]int, len(*w.Vulnerabilities))
	out := make([]KEVEntry, 0, len(*w.Vulnerabilities))
	for i, e := range *w.Vulnerabilities {
		if !cvePattern.MatchString(e.CVEID) {
			return nil, fmt.Errorf("entry %d: %q is no CVE identifier", i, clip(e.CVEID))
		}
		added, err := time.Parse(kevDate, e.DateAdded)
		if err != nil {
			return nil, fmt.Errorf("entry %d (%s): dateAdded is not a date", i, e.CVEID)
		}
		var due time.Time
		if e.DueDate != "" {
			if due, err = time.Parse(kevDate, e.DueDate); err != nil {
				return nil, fmt.Errorf("entry %d (%s): dueDate is not a date", i, e.CVEID)
			}
		}
		k := KEVEntry{CVE: e.CVEID, DateAdded: added, DueDate: due, Ransomware: cleanText(e.KnownRansomwareCampaignUse, 64)}
		if j, dup := byID[k.CVE]; dup {
			if k.DateAdded.Before(out[j].DateAdded) {
				out[j] = k
			}
			continue
		}
		byID[k.CVE] = len(out)
		out = append(out, k)
	}
	return out, nil
}
