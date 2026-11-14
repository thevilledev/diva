package diva

import (
	"bytes"
	"fmt"
	"math/bits"
)

// check verifies the structural invariants of an infix store that spans the
// given number of quotients.
func (s *store) check(quotients int) error {
	occ, re := s.occupieds(), s.runends()
	n := int(s.nslots)
	if got := len(s.words); got != occWords+(n+63)/64+(n*int(s.width)+63)/64 {
		return fmt.Errorf("store has %d words for %d slots of width %d", got, n, s.width)
	}
	set := func(ws []uint64) (n int) {
		for _, w := range ws {
			n += bits.OnesCount64(w)
		}
		return n
	}
	if o, r := set(occ), set(re); o != r {
		return fmt.Errorf("%d occupied quotients but %d run ends", o, r)
	}

	q := nextSet(occ, 0)
	count, inRun := 0, false
	var prev uint64
	for i := range n {
		e := s.slot(i)
		if e == 0 {
			if inRun {
				return fmt.Errorf("slot %d: empty slot inside a run", i)
			}
			if testBit(re, i) {
				return fmt.Errorf("slot %d: run end on an empty slot", i)
			}
			continue
		}
		if q >= quotients {
			return fmt.Errorf("slot %d: entry of quotient %d, store spans %d", i, q, quotients)
		}
		if inRun && compareEntries(prev, e) > 0 {
			return fmt.Errorf("slot %d: run of quotient %d is not sorted", i, q)
		}
		count++
		inRun, prev = true, e
		if testBit(re, i) {
			inRun = false
			q = nextSet(occ, q+1)
		}
	}
	switch {
	case inRun:
		return fmt.Errorf("last run has no end")
	case q < storeTarget:
		return fmt.Errorf("quotient %d is occupied but has no run", q)
	case count != int(s.count):
		return fmt.Errorf("count is %d, found %d entries", s.count, count)
	}
	return nil
}

// check verifies the structural invariants of a filter.
func (f *Filter) check() error {
	last := len(f.samples) - 1
	if last < 0 {
		if len(f.heads) != 0 {
			return fmt.Errorf("%d heads without a sample", len(f.heads))
		}
		if len(f.below)+len(f.above) != 0 {
			return fmt.Errorf("buffered keys without a sample")
		}
		return nil
	}
	if len(f.heads) != len(f.samples) {
		return fmt.Errorf("%d heads for %d samples", len(f.heads), len(f.samples))
	}
	for i := range f.samples {
		s := &f.samples[i]
		if f.heads[i] != wordAt(s.key, 0) {
			return fmt.Errorf("sample %d: head %#x does not match key %x", i, f.heads[i], s.key)
		}
		if !bytes.Equal(s.key, canon(s.key)) {
			return fmt.Errorf("sample %d: key %x is not canonical", i, s.key)
		}
		if i == last {
			if s.store != nil {
				return fmt.Errorf("last sample has a store")
			}
			break
		}
		next := f.samples[i+1].key
		if bytes.Compare(s.key, next) >= 0 {
			return fmt.Errorf("samples %d and %d are out of order", i, i+1)
		}
		if s.store == nil {
			return fmt.Errorf("sample %d has no store", i)
		}
		if want := newSpan(s.key, next); s.span != want {
			return fmt.Errorf("sample %d: span is %+v, want %+v", i, s.span, want)
		}
		if q := s.span.quotients; q < storeTarget/2 || q > storeTarget {
			return fmt.Errorf("sample %d: span has %d quotients", i, q)
		}
		if int(s.store.width) != f.width {
			return fmt.Errorf("sample %d: store width is %d, want %d", i, s.store.width, f.width)
		}
		if s.store.count >= s.store.nslots {
			return fmt.Errorf("sample %d: store is full", i)
		}
		if err := s.store.check(int(s.span.quotients)); err != nil {
			return fmt.Errorf("sample %d: %w", i, err)
		}
	}
	for i := 1; i < last; i++ {
		if !f.samples[i].present {
			return fmt.Errorf("interior sample %d is not present", i)
		}
	}
	for i, k := range f.below {
		if i > 0 && bytes.Compare(f.below[i-1], k) > 0 {
			return fmt.Errorf("below is not sorted at %d", i)
		}
		if bytes.Compare(k, f.samples[0].key) >= 0 {
			return fmt.Errorf("below[%d] is not below the first sample", i)
		}
	}
	for i, k := range f.above {
		if i > 0 && bytes.Compare(f.above[i-1], k) > 0 {
			return fmt.Errorf("above is not sorted at %d", i)
		}
		if bytes.Compare(k, f.samples[last].key) < 0 {
			return fmt.Errorf("above[%d] is below the last sample", i)
		}
	}
	if f.keys < 0 {
		return fmt.Errorf("negative key count %d", f.keys)
	}
	return nil
}
