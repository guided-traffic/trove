package audit

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Category groups event types (R-OPS-014).
type Category string

// The categories of R-OPS-014.
const (
	CategoryAuth       Category = "auth"
	CategoryToken      Category = "token"
	CategoryIdentity   Category = "identity"
	CategoryAuthz      Category = "authz"
	CategoryRepository Category = "repository"
	CategoryContent    Category = "content"
	CategorySystem     Category = "system"
	CategoryConfig     Category = "config"
	CategoryWebhook    Category = "webhook"
)

// Mode is how an event is written (R-OPS-015).
type Mode string

// The audit modes.
const (
	// Transactional events record state changes and are inserted in the
	// transaction of the change.
	Transactional Mode = "transactional"
	// Async events record access and go through the bounded buffer.
	Async Mode = "async"
	// None events are not stored as audit rows.
	None Mode = "none"
)

// Journal is the journal flag of an event type (R-OPS-014).
type Journal string

// The journal values.
const (
	// NoJournal is the absent flag.
	NoJournal Journal = ""
	// JournalRevocation marks an event the revocation journal copies. Only a
	// Transactional type may carry it.
	JournalRevocation Journal = "revocation"
)

// EntryRule says whether a producer must pass a journal entry with an event of
// a flagged type (R-OPS-014 "Required or optional entry").
type EntryRule string

// The entry rules.
const (
	// EntryRequired is the default for a flagged type: an event written without
	// an entry fails its transaction.
	EntryRequired EntryRule = "required"
	// EntryOptional is for types that several producers share.
	EntryOptional EntryRule = "optional"
)

// EventType is one entry of the event registry.
type EventType struct {
	// Name is "<domain>.<event>" or "<domain>.<object>.<event>", lower snake case
	// per segment, the last in past tense (R-OPS-014). Names are a public
	// contract and are never reused.
	Name     string
	Category Category
	Mode     Mode
	// Webhook says whether subscriptions may select the event (R-OPS-037).
	Webhook bool
	// Journal is the revocation flag. It means "journaled when the producer
	// supplies an entry".
	Journal Journal
	// Entry is the entry rule of a flagged type; empty means EntryRequired.
	Entry EntryRule
	// Schema is the JSON Schema of the event data (R-OPS-014); every array in it
	// declares maxItems of at most 1000.
	Schema string
}

// sharedTypes are the event types that several producers share and that may
// therefore declare an optional journal entry (R-OPS-014): npm journals neither
// of them, NuGet journals both for its local repositories.
var sharedTypes = map[string]bool{"package.published": true, "package.deleted": true}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,2}$`)

var reg = struct {
	mu     sync.RWMutex
	by     map[string]EventType
	frozen bool
}{by: map[string]EventType{}}

// Register adds an event type. It is meant for the package initialisation of
// the events_<area>.go files and panics on a programming error: an invalid
// name, an unknown category or mode, a duplicate, a journal flag on a type that
// is not transactional, an empty schema, or a registration after [Freeze].
func Register(t EventType) {
	if !namePattern.MatchString(t.Name) {
		panic(fmt.Sprintf("audit: invalid event name %q", t.Name))
	}
	switch t.Category {
	case CategoryAuth, CategoryToken, CategoryIdentity, CategoryAuthz, CategoryRepository,
		CategoryContent, CategorySystem, CategoryConfig, CategoryWebhook:
	default:
		panic(fmt.Sprintf("audit: event %q has category %q", t.Name, t.Category))
	}
	switch t.Mode {
	case Transactional, Async, None:
	default:
		panic(fmt.Sprintf("audit: event %q has mode %q", t.Name, t.Mode))
	}
	switch t.Journal {
	case NoJournal:
	case JournalRevocation:
		if t.Mode != Transactional {
			panic(fmt.Sprintf("audit: event %q carries a journal flag but is not transactional", t.Name))
		}
	default:
		panic(fmt.Sprintf("audit: event %q has journal %q", t.Name, t.Journal))
	}
	switch t.Entry {
	case "", EntryRequired, EntryOptional:
	default:
		panic(fmt.Sprintf("audit: event %q has journal entry rule %q", t.Name, t.Entry))
	}
	if t.Entry != "" && t.Journal != JournalRevocation {
		panic(fmt.Sprintf("audit: event %q declares a journal entry rule without the journal flag", t.Name))
	}
	if t.Entry == EntryOptional && !sharedTypes[t.Name] {
		panic(fmt.Sprintf("audit: event %q is not a shared type and cannot declare an optional journal entry", t.Name))
	}
	if t.Journal == JournalRevocation && t.Entry == "" {
		t.Entry = EntryRequired
	}
	if t.Schema == "" {
		panic(fmt.Sprintf("audit: event %q has no data schema", t.Name))
	}
	if err := checkSchema(t.Schema); err != nil {
		panic(fmt.Sprintf("audit: event %q has a bad data schema: %v", t.Name, err))
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.frozen {
		panic(fmt.Sprintf("audit: event %q registered after Freeze", t.Name))
	}
	if _, dup := reg.by[t.Name]; dup {
		panic(fmt.Sprintf("audit: event %q is registered twice", t.Name))
	}
	reg.by[t.Name] = t
}

// Freeze closes the registry before the replica serves.
func Freeze() {
	reg.mu.Lock()
	reg.frozen = true
	reg.mu.Unlock()
}

// Lookup returns a registered event type.
func Lookup(name string) (EventType, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	t, ok := reg.by[name]
	return t, ok
}

// Types lists the registered event types, sorted by name.
func Types() []EventType {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]EventType, 0, len(reg.by))
	for _, t := range reg.by {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
