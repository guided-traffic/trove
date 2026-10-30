package formathost

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/guided-traffic/trove/internal/formats"
)

// Bindings are the data ports of the formats, one value per format ID. The
// composition root binds them - it alone imports a format's data package
// (internal/formatdata/<id>), which this package may not (archtest, Q-ARCH-193) -
// and [Bindings.Check] refuses to start the replica when a binding is missing,
// does not implement the format's DataPort, or belongs to no format.
//
// A binding is made once, before the replica serves; the lock only keeps a test
// that binds from another goroutine honest.
type Bindings struct {
	mu sync.RWMutex
	by map[formats.ID]any
}

// NewBindings returns an empty set.
func NewBindings() *Bindings { return &Bindings{by: map[formats.ID]any{}} }

// Bind binds the port of a format. A nil port and a second binding of one ID are
// errors; whether the port fits the format is [Bindings.Check]'s question, which
// needs the registry.
func (b *Bindings) Bind(id formats.ID, port any) error {
	if port == nil {
		return fmt.Errorf("formathost: the data port of %q is nil", id)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, dup := b.by[id]; dup {
		return fmt.Errorf("formathost: the data port of %q is bound twice", id)
	}
	b.by[id] = port
	return nil
}

// Get returns the port bound to a format, nil when there is none.
func (b *Bindings) Get(id formats.ID) any {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.by[id]
}

// Check verifies the bindings against the descriptors of the given formats: every
// DataPort has a binding that implements it, and no binding is left over. The
// error names every format at fault, so a start-up failure says what to bind.
func (b *Bindings) Check(fs []formats.Format) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var errs []error
	seen := map[formats.ID]bool{}
	for _, f := range fs {
		d := f.Descriptor()
		seen[d.ID] = true
		port, bound := b.by[d.ID]
		switch {
		case d.DataPort == nil && bound:
			errs = append(errs, fmt.Errorf("format %q declares no data port and one is bound", d.ID))
		case d.DataPort == nil:
		case !bound:
			errs = append(errs, fmt.Errorf("format %q declares the data port %v and none is bound", d.ID, d.DataPort))
		case !reflect.TypeOf(port).Implements(d.DataPort):
			errs = append(errs, fmt.Errorf("the data port bound to format %q is a %T and does not implement %v", d.ID, port, d.DataPort))
		}
	}
	var extra []string
	for id := range b.by {
		if !seen[id] {
			extra = append(extra, string(id))
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		errs = append(errs, fmt.Errorf("a data port is bound to %q, which is no registered format", id))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("formats: data ports: %w", errors.Join(errs...))
}
