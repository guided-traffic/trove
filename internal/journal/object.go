package journal

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Version is the format number of a journal object.
const Version = 1

// Dir is the key family of the journal below the blob prefix (R-STOR-001); the orphan sweep leaves it alone.
const Dir = "_journal/revocations"

// Object is one journal object: the envelope of R-HA-054 around the producer's entry. ID is the audit event's ID (a
// UUIDv7, so ids sort by time), At the transaction's now(), Event the registry name. Entry is the producer's JSON object
// verbatim; for an update it carries the object's state before and after the change (the update rule), for a removal the
// IDs of the subject.
type Object struct {
	V     int             `json:"v"`
	ID    string          `json:"id"`
	At    time.Time       `json:"at"`
	Event string          `json:"event"`
	Entry json.RawMessage `json:"entry"`
}

// OutboxRow is a row of revocation_outbox.
type OutboxRow struct {
	ID    string
	At    time.Time
	Event string
	Entry json.RawMessage
}

// NewObject wraps an outbox row.
func NewObject(r OutboxRow) Object {
	return Object{V: Version, ID: r.ID, At: r.At.UTC(), Event: r.Event, Entry: r.Entry}
}

// Encode is the JSON form of an object. The bytes depend only on the row, so a row shipped twice (its object written, its
// deletion not committed) produces the same object under the same key (R-HA-054).
func (o Object) Encode() ([]byte, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(o)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (o Object) validate() error {
	var errs []error
	if o.V < 1 {
		errs = append(errs, errors.New("v must be at least 1"))
	}
	if !uuidPattern.MatchString(o.ID) {
		errs = append(errs, fmt.Errorf("id %q is not a lowercase UUID", o.ID))
	}
	if o.At.IsZero() || o.Event == "" {
		errs = append(errs, errors.New("at and event are required"))
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(o.Entry, &probe); err != nil {
		errs = append(errs, errors.New("entry must be a JSON object"))
	}
	return errors.Join(errs...)
}

// ErrUnknownVersion is an object of a higher version than this binary reads. The replay reports it and skips the object.
var ErrUnknownVersion = errors.New("journal: object has a newer version than this trove reads")

// Decode parses and validates an object. Anyone who holds the runtime S3 credential can write journal objects, so a
// decoded object is untrusted input: the handlers treat it as a claim to be checked against the restored database
// (R-HA-054 "Forged entries cannot escalate").
func Decode(raw []byte) (Object, error) {
	var o Object
	if err := json.Unmarshal(raw, &o); err != nil {
		return Object{}, fmt.Errorf("journal: %w", err)
	}
	if o.V > Version {
		return Object{}, fmt.Errorf("%w: v %d", ErrUnknownVersion, o.V)
	}
	if err := o.validate(); err != nil {
		return Object{}, fmt.Errorf("journal: %w", err)
	}
	return o, nil
}

// Less orders objects as the replay applies them: by at, then by id (R-HA-054).
func (o Object) Less(p Object) bool {
	if !o.At.Equal(p.At) {
		return o.At.Before(p.At)
	}
	return o.ID < p.ID
}

// ListPrefix is the key prefix of the whole journal for a blob prefix, with a trailing slash.
func ListPrefix(blobPrefix string) string {
	if blobPrefix == "" {
		return Dir + "/"
	}
	return blobPrefix + "/" + Dir + "/"
}

// Key is the object key: <prefix>/_journal/revocations/<yyyy>/<mm>/<dd>/<id>.json, the date being the UTC date of at.
func Key(blobPrefix string, o Object) string {
	return fmt.Sprintf("%s%s/%s.json", ListPrefix(blobPrefix), o.At.UTC().Format("2006/01/02"), o.ID)
}

// ParseKey returns the entry ID of a journal key, and false for a key that is not one.
func ParseKey(blobPrefix, key string) (id string, ok bool) {
	rest, found := strings.CutPrefix(key, ListPrefix(blobPrefix))
	if !found {
		return "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || !strings.HasSuffix(parts[3], ".json") {
		return "", false
	}
	id = strings.TrimSuffix(parts[3], ".json")
	return id, uuidPattern.MatchString(id)
}

// DayPrefixes returns the key prefixes of the UTC days from through to inclusive, so a reader lists only the days it needs
// instead of the whole journal. The replay window starts at snapshotAt minus the margin.
func DayPrefixes(blobPrefix string, from, to time.Time) []string {
	var out []string
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		out = append(out, ListPrefix(blobPrefix)+d.Format("2006/01/02")+"/")
	}
	return out
}
