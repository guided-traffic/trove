package index

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// countdown is a ParagraphSink that cancels its context after n paragraphs, as a shutdown does in the middle of a merge.
type countdown struct {
	n      int
	cancel context.CancelFunc
	seen   int
	// cancelled is when the context was cancelled: "soon after the cancellation" is measured from here.
	cancelled time.Time
}

func (c *countdown) Paragraph([]byte) error {
	c.seen++
	if c.seen == c.n {
		c.cancelled = time.Now()
		c.cancel()
	}
	return nil
}

func TestMergeMemberStopsWhenItsContextEnds(t *testing.T) {
	t.Run("R-DEB-025 R-ARCH-024 a merge of a large index returns soon after its context is cancelled", func(t *testing.T) {
		var doc strings.Builder
		for i := range 20000 {
			doc.WriteString(para(fmt.Sprintf("pkg-%d", i), "1.0", "amd64", fmt.Sprintf("pool/p/pkg-%d.deb", i)))
			doc.WriteString("\n")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sink := &countdown{n: 10, cancel: cancel}
		_, err := MergeMember(ctx, sink, NewSeen(0), "m", strings.NewReader(doc.String()), keepAll)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if took := time.Since(sink.cancelled); sink.seen > 10+mergeCheckEvery || took > time.Second {
			t.Fatalf("the merge read up to paragraph %d and took %s after the cancellation at 10", sink.seen, took)
		}
	})
}
