package remote

import (
	"strconv"

	"github.com/guided-traffic/trove/internal/errs"
)

// Budget bounds what one rendering reads of an upstream's registration (Q-NUGET-83): the
// inflated bytes of the index and of every page it reads, together, and the leaves. The bound of
// R-ARCH-028 on one document held in memory applies to the whole rendering, not to each of its
// documents, so an index of many pages costs no more memory than one document of that size. A
// Budget is used by one goroutine; the virtual merge shares one over its remote members.
type Budget struct {
	bytes  int64
	leaves int
}

// NewBudget returns the budget of one rendering: bytes inflated bytes and MaxLeaves leaves.
func NewBudget(bytes int64) *Budget { return &Budget{bytes: bytes, leaves: MaxLeaves} }

// Index reads a stored registration index within the budget.
func (b *Budget) Index(doc []byte) (*Index, error) {
	plain, err := b.decode(doc)
	if err != nil {
		return nil, err
	}
	idx, err := parseIndex(plain)
	if err != nil {
		return nil, err
	}
	for _, p := range idx.Pages {
		if err := b.take(len(p.Leaves)); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// Page reads a stored registration page within the budget.
func (b *Budget) Page(doc []byte) ([]Leaf, error) {
	plain, err := b.decode(doc)
	if err != nil {
		return nil, err
	}
	leaves, err := parsePage(plain)
	if err != nil {
		return nil, err
	}
	if err := b.take(len(leaves)); err != nil {
		return nil, err
	}
	return leaves, nil
}

// decode inflates a document within what is left of the bytes and spends them.
func (b *Budget) decode(doc []byte) ([]byte, error) {
	plain, err := Decode(doc, b.bytes)
	if e, ok := errs.As(err); ok && e.Code == errs.CodeUpstreamItemTooLarge {
		return nil, errs.E(errs.UpstreamFailed, errs.CodeUpstreamItemTooLarge,
			"The upstream's registration index and the pages it lists are together larger than the limit of documents held in memory.")
	}
	if err != nil {
		return nil, err
	}
	b.bytes -= int64(len(plain))
	return plain, nil
}

// take spends n leaves.
func (b *Budget) take(n int) error {
	if n > b.leaves {
		return invalid("The upstream's registration lists more than " + strconv.Itoa(MaxLeaves) + " versions; trove reads at most that many of one ID.")
	}
	b.leaves -= n
	return nil
}
