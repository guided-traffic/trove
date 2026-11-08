package avatar

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// refinementPlan is one DC scan and n AC refinement scans over the components in turn.
func refinementPlan(components, n int) []eobScan {
	all := make([]int, components)
	for c := range all {
		all[c] = c
	}
	plan := []eobScan{{comps: all}}
	for i := range n {
		plan = append(plan, eobScan{comps: []int{i % components}, refine: true})
	}
	return plan
}

// dcPlan is n interleaved DC scans over all the components.
func dcPlan(components, n int) []eobScan {
	all := make([]int, components)
	for c := range all {
		all[c] = c
	}
	plan := make([]eobScan, n)
	for i := range plan {
		plan[i] = eobScan{comps: all}
	}
	return plan
}

func TestR_API_055_RefusesAJPEGWhoseScansAddUpToTooMuchWork(t *testing.T) {
	rgb, cmyk := largestProgressiveSide(3), largestProgressiveSide(4)
	cases := []struct {
		name string
		data []byte
	}{
		{"gray 2048, 1 DC scan and 63 AC refinement scans of a few bytes each", eobJPEG(t, 2048, 1, refinementPlan(1, 63))},
		{"gray 2048, 64 DC scans", eobJPEG(t, 2048, 1, dcPlan(1, 64))},
		{"3 components of the largest accepted size, 64 interleaved DC scans", eobJPEG(t, rgb, 3, dcPlan(3, 64))},
		{"4 components of the largest accepted size, 64 interleaved DC scans", eobJPEG(t, cmyk, 4, dcPlan(4, 64))},
	}
	for _, c := range cases {
		t.Run("R-API-055 "+c.name, func(t *testing.T) {
			if len(c.data) > MaxUpload {
				t.Fatalf("%d bytes: the upload limit refuses it, not the walk", len(c.data))
			}
			// The decoding slot is taken: a request that reached it would wait for BusyWait and answer ErrBusy, so ErrComplex is
			// the refusal by the header, before a slot and before a decode.
			p := newProcessor(1, time.Minute)
			p.slots <- struct{}{}
			start := time.Now()
			if _, err := p.Process(context.Background(), "image/jpeg", c.data); !errors.Is(err, ErrComplex) {
				t.Fatalf("err = %v, want ErrComplex", err)
			}
			if took := time.Since(start); took > time.Second {
				t.Fatalf("the refusal took %s", took)
			}
		})
	}
	t.Run("R-API-055 the plans that spend the whole allowance are inside it, one more scan is not", func(t *testing.T) {
		for _, c := range []struct {
			components int
			refine     bool
		}{{1, true}, {1, false}, {3, false}, {3, true}, {4, false}} {
			side := 2048
			if c.components > 1 {
				side = largestProgressiveSide(c.components)
			}
			plan := eobPlan(side, c.components, c.refine)
			sh, err := walkJPEG(eobJPEG(t, side, c.components, plan))
			if err != nil || sh.scans != len(plan) || sh.visits > MaxBlockVisits {
				t.Fatalf("components %d refine %v: %d scans, %d visits, err %v", c.components, c.refine, sh.scans, sh.visits, err)
			}
			if len(plan) < MaxScans {
				more := eobPlan(side, c.components, c.refine)
				more = append(more, more[len(more)-1])
				if _, err := walkJPEG(eobJPEG(t, side, c.components, more)); !errors.Is(err, ErrComplex) {
					t.Errorf("components %d refine %v: %d scans: %v, want ErrComplex", c.components, c.refine, len(more), err)
				}
			}
		}
	})
	t.Run("R-API-055 a progressive photo of ten scans costs a quarter of the allowance or less", func(t *testing.T) {
		sh, err := walkJPEG(jpegFrame(0xC2, 2048, 2048, 3, 10))
		if err != nil || sh.visits*4 > MaxBlockVisits {
			t.Fatalf("%d visits, err %v", sh.visits, err)
		}
	})
}

// TestR_API_055_StopsAScanHeavyDecodeAtItsDeadline: a JPEG that carries its scans in a few bytes is read in a handful of reads
// of a decoder that fills 4 KiB at a time, and the deadline would never be looked at while the scans run. The reads of the
// decoder's input are short, so it is.
func TestR_API_055_StopsAScanHeavyDecodeAtItsDeadline(t *testing.T) {
	data := eobJPEG(t, 2048, 1, eobPlan(2048, 1, true))
	if len(data) > 16<<10 {
		t.Fatalf("%d bytes: the input is meant to be small", len(data))
	}
	for _, deadline := range []time.Duration{5 * time.Millisecond, 20 * time.Millisecond} {
		p := proc()
		p.decodeTimeout = deadline
		start := time.Now()
		_, err := p.Process(context.Background(), "image/jpeg", data)
		if !errors.Is(err, ErrComplex) {
			t.Fatalf("deadline %s: err = %v after %s, want ErrComplex", deadline, err, time.Since(start))
		}
	}
}

func TestR_API_055_ReadsTheInputInShortPieces(t *testing.T) {
	r := &ctxReader{ctx: context.Background(), r: bytes.NewReader(make([]byte, 1000))}
	if n, err := r.Read(make([]byte, 4096)); err != nil || n != readChunk {
		t.Fatalf("read %d bytes, err %v, want %d", n, err, readChunk)
	}
}
