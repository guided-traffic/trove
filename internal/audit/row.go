package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// Errors a producer can match with errors.Is. All of them are programming
// errors: a registered type is used in the mode it declares, with the entry its
// journal rule asks for.
var (
	// ErrUnknownType is an event whose type is not in the registry.
	ErrUnknownType = errors.New("audit: event type is not registered")
	// ErrWrongMode is RecordTx on an async type or Record on a transactional one.
	ErrWrongMode = errors.New("audit: event type is recorded in the other mode")
	// ErrJournalEntryRequired is an event of a flagged type with EntryRequired
	// and no journal entry: the transaction fails (R-OPS-014).
	ErrJournalEntryRequired = errors.New("audit: the event type requires a journal entry")
	// ErrJournalNotAllowed is a journal entry on an event of an unflagged type.
	ErrJournalNotAllowed = errors.New("audit: the event type carries no journal flag")
	// ErrInvalidEvent is an event the table cannot hold: a bad outcome, details or
	// journal entry that is not a JSON object, a missing actor kind.
	ErrInvalidEvent = errors.New("audit: invalid event")
)

// Size bounds of attacker-influenced columns, in bytes. Details and the journal
// entry are bounded by the schemas of their types (arrays hold at most 1,000
// items, R-OPS-014); maxDetails is the backstop against a producer bug.
const (
	maxText    = 1024
	maxLongTxt = 4096
	maxDetails = 1 << 20
)

// row is one audit_event row as the insert statement takes it: text only, the
// empty string standing for NULL, and the time as RFC 3339 text (empty means
// the transaction's now()). The JSON names are the keys the statement reads.
type row struct {
	ID           string          `json:"id"`
	At           string          `json:"at"`
	Action       string          `json:"action"`
	Category     string          `json:"category"`
	Outcome      string          `json:"outcome"`
	ActorType    string          `json:"actor_type"`
	ActorID      string          `json:"actor_id"`
	ActorName    string          `json:"actor_name"`
	TokenID      string          `json:"token_id"`
	AuthMethod   string          `json:"auth_method"`
	SourceIP     string          `json:"source_ip"`
	UserAgent    string          `json:"user_agent"`
	TraceID      string          `json:"trace_id"`
	Replica      string          `json:"replica"`
	Endpoint     string          `json:"endpoint"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Repository   string          `json:"repository"`
	Path         string          `json:"path"`
	Reason       string          `json:"reason"`
	Details      json.RawMessage `json:"details"`
}

// toRow validates ev against its type and converts it. at is the event's time;
// the zero time leaves it to the database (the transaction's now()).
func toRow(t EventType, ev Event, id string, at time.Time, replica string) (row, error) {
	r := row{
		ID: id, Action: t.Name, Category: string(t.Category), Replica: clip(replica, maxText),
		ActorType: actorType(ev.Actor.Kind),
		ActorName: clip(ev.Actor.Name, maxText), AuthMethod: clip(ev.Actor.AuthMethod, maxText),
		ActorID: uuidOrEmpty(ev.Actor.ID), TokenID: uuidOrEmpty(ev.Actor.TokenID),
		SourceIP: addrOrEmpty(ev.Source.ClientAddr), UserAgent: clip(ev.Source.UserAgent, maxLongTxt),
		TraceID: clip(ev.Source.TraceID, maxText), Endpoint: clip(ev.Endpoint, maxText),
		ResourceType: clip(ev.Subject.Kind, maxText), ResourceID: clip(firstNonEmpty(ev.Subject.ID, ev.Subject.Name), maxText),
		Repository: clip(ev.Repository, maxText), Path: clip(ev.Path, maxLongTxt), Reason: clip(ev.Reason, maxText),
	}
	if !at.IsZero() {
		r.At = at.UTC().Format(time.RFC3339Nano)
	}
	if r.Repository == "" && ev.Subject.Kind == "repository" {
		r.Repository = clip(firstNonEmpty(ev.Subject.Name, ev.Subject.ID), maxText)
	}
	if r.ActorType == "" {
		return r, fmt.Errorf("%w: %s has no actor kind", ErrInvalidEvent, t.Name)
	}
	switch o := ev.Outcome; o {
	case Success, Failure, Denied:
		r.Outcome = string(o)
	case "":
		r.Outcome = string(Success)
	default:
		return r, fmt.Errorf("%w: %s has outcome %q", ErrInvalidEvent, t.Name, o)
	}
	d, err := object(ev.Details)
	if err != nil {
		return r, fmt.Errorf("%w: %s details: %w", ErrInvalidEvent, t.Name, err)
	}
	r.Details = d
	return r, nil
}

// object returns raw as a JSON object, {} for none, and reports anything else.
func object(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage("{}"), nil
	}
	if len(raw) > maxDetails {
		return nil, fmt.Errorf("%d bytes exceed the limit of %d", len(raw), maxDetails)
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, errors.New("not a JSON object")
	}
	return raw, nil
}

// actorType is the actor_type column: the contract's kind, with the break-glass
// spelling of the table.
func actorType(kind string) string {
	if kind == "break_glass" {
		return "breakglass"
	}
	return clip(kind, maxText)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// uuidOrEmpty keeps a canonical UUID and drops anything else: the column is a
// uuid, and an actor that has no UUID (a system actor) is named in actor_name.
func uuidOrEmpty(s string) string {
	if _, ok := parseUUID(strings.ToLower(s)); ok {
		return strings.ToLower(s)
	}
	return ""
}

// addrOrEmpty returns the IP of a client address, with or without a port, and
// the empty string for anything else.
func addrOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().String()
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().String()
	}
	return ""
}

// clip cuts s to at most n bytes on a rune boundary and removes NUL, which
// PostgreSQL text cannot hold.
func clip(s string, n int) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}
