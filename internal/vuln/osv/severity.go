package osv

import (
	"errors"
	"fmt"
	"math"
	"strings"

	gocvss20 "github.com/pandatix/go-cvss/20"
	gocvss30 "github.com/pandatix/go-cvss/30"
	gocvss31 "github.com/pandatix/go-cvss/31"
	gocvss40 "github.com/pandatix/go-cvss/40"

	"github.com/guided-traffic/trove/internal/vuln/advisory"
)

// Vector is one CVSS vector of a record that parsed, with its score and the level of its version's rating scale
// (R-VULN-007).
type Vector struct {
	// Version is "4.0", "3.1", "3.0" or "2.0".
	Version string
	Vector  string
	// Score is the base score, to one decimal.
	Score float64
	Level advisory.Severity
}

// CVSS returns the vector as the advisory's scored vector.
func (v Vector) CVSS() advisory.CVSS {
	return advisory.CVSS{Version: v.Version, Vector: v.Vector, Score: v.Score}
}

// versionOrder is the order the vector of a record is taken in: the newest CVSS version present first (R-VULN-007 rule 2).
func versionOrder() []string { return []string{"4.0", "3.1", "3.0", "2.0"} }

// maxVectorLen bounds a vector string before it reaches the parser; the longest 4.0 vector with every metric is below 300
// bytes.
const maxVectorLen = 512

// ErrVector is wrapped by the error of a vector that does not parse.
var ErrVector = errors.New("osv: not a CVSS vector")

// ScoreVector parses and scores one vector with github.com/pandatix/go-cvss and rates it on the scale of its version
// (R-VULN-007): the version is read from the vector's header (CVSS:4.0/, CVSS:3.1/, CVSS:3.0/), and a vector without a header is
// CVSS 2.0. The score is the base score.
func ScoreVector(vector string) (Vector, error) {
	if len(vector) == 0 || len(vector) > maxVectorLen {
		return Vector{}, fmt.Errorf("%w: length %d", ErrVector, len(vector))
	}
	var (
		v   Vector
		err error
	)
	switch {
	case strings.HasPrefix(vector, "CVSS:4.0/"):
		var c *gocvss40.CVSS40
		if c, err = gocvss40.ParseVector(vector); err == nil {
			v = Vector{Version: "4.0", Vector: c.Vector(), Score: c.Score()}
			v.Level, err = rate(v.Score, gocvss40.Rating)
		}
	case strings.HasPrefix(vector, "CVSS:3.1/"):
		var c *gocvss31.CVSS31
		if c, err = gocvss31.ParseVector(vector); err == nil {
			v = Vector{Version: "3.1", Vector: c.Vector(), Score: c.BaseScore()}
			v.Level, err = rate(v.Score, gocvss31.Rating)
		}
	case strings.HasPrefix(vector, "CVSS:3.0/"):
		var c *gocvss30.CVSS30
		if c, err = gocvss30.ParseVector(vector); err == nil {
			v = Vector{Version: "3.0", Vector: c.Vector(), Score: c.BaseScore()}
			v.Level, err = rate(v.Score, gocvss30.Rating)
		}
	case !strings.HasPrefix(vector, "CVSS:"):
		var c *gocvss20.CVSS20
		if c, err = gocvss20.ParseVector(vector); err == nil {
			v = Vector{Version: "2.0", Vector: c.Vector(), Score: c.BaseScore(), Level: rating20(c.BaseScore())}
		}
	default:
		err = errors.New("unknown version header")
	}
	if err != nil {
		return Vector{}, fmt.Errorf("%w: %w", ErrVector, err)
	}
	v.Score = math.Round(v.Score*10) / 10
	return v, nil
}

// rate maps the library's qualitative rating of a score to a level.
func rate(score float64, rating func(float64) (string, error)) (advisory.Severity, error) {
	r, err := rating(score)
	if err != nil {
		return advisory.SeverityUnknown, err
	}
	switch strings.ToUpper(r) {
	case "NONE":
		return advisory.SeverityNone, nil
	case "LOW":
		return advisory.SeverityLow, nil
	case "MEDIUM":
		return advisory.SeverityMedium, nil
	case "HIGH":
		return advisory.SeverityHigh, nil
	case "CRITICAL":
		return advisory.SeverityCritical, nil
	}
	return advisory.SeverityUnknown, fmt.Errorf("rating %q", r)
}

// rating20 is the qualitative scale of CVSS 2.0 (NVD): Low 0.0 to 3.9, Medium 4.0 to 6.9, High 7.0 to 10.0. It has no None and
// no Critical.
func rating20(score float64) advisory.Severity {
	switch {
	case score >= 7.0:
		return advisory.SeverityHigh
	case score >= 4.0:
		return advisory.SeverityMedium
	}
	return advisory.SeverityLow
}

// Own is the severity a record decides for itself, by rules 1, 2, 4 and 5 of R-VULN-007; rule 3, the alias records, needs
// the rows of the alias group and is [Decide]'s.
type Own struct {
	Level  advisory.Severity
	Source advisory.SeveritySource
	CVSS   advisory.CVSS
}

// DecideOwn applies R-VULN-007 to one record: malicious is critical; else the newest CVSS version among its vectors; else the
// qualitative `database_specific.severity` as a fallback; else unknown.
func DecideOwn(malicious bool, vectors []Vector, qualitative advisory.Severity) Own {
	if malicious {
		return Own{Level: advisory.SeverityCritical, Source: advisory.SourceMalicious}
	}
	if v, ok := Newest(vectors); ok {
		return Own{Level: v.Level, Source: advisory.SourceCVSS, CVSS: v.CVSS()}
	}
	if qualitative.Valid() && qualitative != advisory.SeverityNone && qualitative != advisory.SeverityUnknown {
		return Own{Level: qualitative, Source: advisory.SourceAdvisory}
	}
	return Own{Level: advisory.SeverityUnknown, Source: advisory.SourceNone}
}

// Newest returns the first vector of the newest CVSS version among vectors, in record order (R-VULN-007 rule 2).
func Newest(vectors []Vector) (Vector, bool) {
	for _, ver := range versionOrder() {
		for _, v := range vectors {
			if v.Version == ver {
				return v, true
			}
		}
	}
	return Vector{}, false
}

// Decide applies rule 3 of R-VULN-007 to the canonical record's own decision: when the canonical record carries no CVSS vector
// (and is not malicious), the alias records with a vector decide by rule 2, and when several do, the highest level wins (a
// tie takes the higher score, then the first in the order given). Without an alias vector the own decision stands.
func Decide(canonical Own, aliases []Own) Own {
	if canonical.Source == advisory.SourceCVSS || canonical.Source == advisory.SourceMalicious {
		return canonical
	}
	best := Own{}
	found := false
	for _, a := range aliases {
		if a.Source != advisory.SourceCVSS {
			continue
		}
		if !found || a.Level.Higher(best.Level) || (a.Level == best.Level && a.CVSS.Score > best.CVSS.Score) {
			best, found = a, true
		}
	}
	if found {
		return best
	}
	return canonical
}
