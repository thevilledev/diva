package diva

import (
	"cmp"
	"math/bits"
	"slices"
	"sort"
)

const (
	// quotientBits is log2 of the number of keys Diva aims to keep between two
	// adjacent samples. Infixes are derived so that every store sees between
	// storeTarget/2 and storeTarget distinct quotients.
	quotientBits = 10
	storeTarget  = 1 << quotientBits

	occWords = storeTarget / 64
	minSlots = 8

	// maxProbe bounds how far an insert looks below a run for an empty slot
	// when it already knows of one above.
	maxProbe = 64
)

// An entry is the unit an infix store holds. It is an infix split into a
// quotient, the high bits that select a run, and a remainder, the low width
// bits kept in a slot:
//
//	entry = quotient<<width | remainder
//
// The remainder ends in a unary length marker: its lowest set bit is a
// delimiter and only the bits above it are key bits. A full-length entry has
// the delimiter at bit 0. An entry that lost resolution in a split has it
// higher and stands for every full-length entry sharing the bits above the
// delimiter. Because the delimiter is always present a remainder is never
// zero, so a zero slot is an empty slot.

// delimiter isolates the unary length marker of an entry.
func delimiter(e uint64) uint64 { return e & -e }

// floorOf and ceilOf bound the full-length entries that e stands for.
func floorOf(e uint64) uint64 { return e - delimiter(e) }
func ceilOf(e uint64) uint64  { return e | (delimiter(e) - 1) }

// compareEntries orders entries by the start of the range they stand for,
// placing the wider of two entries that start together first. Stores keep
// their entries in this order.
func compareEntries(a, b uint64) int {
	if c := cmp.Compare(floorOf(a), floorOf(b)); c != 0 {
		return c
	}
	return cmp.Compare(delimiter(b), delimiter(a))
}

// store is an infix store: the entries of the keys that fall between two
// adjacent samples.
//
// Entries with the same quotient form a run of adjacent slots. Runs appear
// in quotient order, and the i-th set bit of occupieds corresponds to the
// i-th set bit of runends, which marks the last slot of that run. The slot
// array is overprovisioned so that runs sit near the position their quotient
// maps to and an insert only has to move the entries up to the nearest empty
// slot.
type store struct {
	// words holds three consecutive regions: occupieds, one bit per
	// quotient; runends, one bit per slot; and the packed slots.
	words  []uint64
	nslots int32
	count  int32
	width  uint8 // bits per slot, including the delimiter
}

func newStore(width, nslots int) *store {
	return &store{
		words:  make([]uint64, occWords+(nslots+63)/64+(nslots*width+63)/64),
		nslots: int32(nslots),
		width:  uint8(width),
	}
}

func (s *store) occupieds() []uint64 { return s.words[:occWords] }

func (s *store) runends() []uint64 {
	return s.words[occWords : occWords+(int(s.nslots)+63)/64]
}

// slotPos returns the position in words of the first bit of slot i.
func (s *store) slotPos(i int) int {
	return (occWords+(int(s.nslots)+63)>>6)<<6 + i*int(s.width)
}

func (s *store) slot(i int) uint64 {
	return getBits(s.words, s.slotPos(i), int(s.width))
}

func (s *store) setSlot(i int, v uint64) {
	setBits(s.words, s.slotPos(i), int(s.width), v)
}

// shiftSlotsUp moves slots [lo, hi) one position up; shiftSlotsDown moves
// them one position down. The slot that falls out of the range is zeroed.
func (s *store) shiftSlotsUp(lo, hi int) {
	shiftUp(s.words, s.slotPos(lo), s.slotPos(hi), int(s.width))
}

func (s *store) shiftSlotsDown(lo, hi int) {
	shiftDown(s.words, s.slotPos(lo), s.slotPos(hi), int(s.width))
}

// firstUsed returns the first non-empty slot in [lo, hi]. Slot hi must be in
// use and the used slots of the range must form its tail, which holds for any
// range that starts right after a run end and stops inside the next run.
func (s *store) firstUsed(lo, hi int) int {
	// Runs are a slot or two long unless many keys share a quotient, so look
	// at the nearest slots before falling back to a binary search.
	for range 4 {
		if hi == lo || s.slot(hi-1) == 0 {
			return hi
		}
		hi--
	}
	return lo + sort.Search(hi-lo, func(i int) bool { return s.slot(lo+i) != 0 })
}

// run returns the first and last slot of the run of quotient q, which must be
// occupied.
func (s *store) run(q int) (start, end int) {
	re := s.runends()
	end = selectBit(re, rank(s.occupieds(), q))
	return s.firstUsed(prevSet(re, end-1)+1, end), end
}

// slotTops[w] has the top bit of each of the slots of w bits that fit in a
// word. It lets the searches for an empty slot test a word's worth of slots
// at a time.
var slotTops = func() (tops [maxFingerprintBits + 2]uint64) {
	for w := 1; w < len(tops); w++ {
		for i := range 64 / w {
			tops[w] |= 1 << (i*w + w - 1)
		}
	}
	return tops
}()

// nextEmpty returns the first empty slot at or after i, or nslots.
func (s *store) nextEmpty(i int) int {
	w, n := int(s.width), int(s.nslots)
	tops := slotTops[w]
	for per := bits.OnesCount64(tops); i < n; i += per {
		c := min(per, n-i)
		t := tops & mask(c*w)
		if z := zeroFields(getBits(s.words, s.slotPos(i), c*w), t, w); z != 0 {
			// Slots are counted by their top bits below the first empty one.
			return i + bits.OnesCount64(t&(z&-z-1))
		}
	}
	return n
}

// emptyBelow returns the last empty slot in (floor, i], or -1.
func (s *store) emptyBelow(i, floor int) int {
	w := int(s.width)
	tops := slotTops[w]
	for per := bits.OnesCount64(tops); i > floor; i -= per {
		c := min(per, i-floor)
		t := tops & mask(c*w)
		if z := zeroFields(getBits(s.words, s.slotPos(i-c+1), c*w), t, w); z != 0 {
			last := uint64(1) << (63 - bits.LeadingZeros64(z))
			return i - c + 1 + bits.OnesCount64(t&(last-1))
		}
	}
	return -1
}

// roomBelow decides which way an insert shifts. up is the first empty slot
// above, or nslots if there is none. It returns the last empty slot at or
// before i if that is within reach slots of i, which is how the caller says
// where shifting down stops being the shorter move, and -1 to shift up
// otherwise. With no room above it returns the nearest empty slot below,
// however far.
func (s *store) roomBelow(i, up, reach int) int {
	if up == int(s.nslots) {
		return s.emptyBelow(i, -1)
	}
	return s.emptyBelow(i, max(i-min(reach, maxProbe), -1))
}

// insert adds entry v. quotients is the number of quotient values the store
// spans and only steers placement. There must be a free slot.
func (s *store) insert(v uint64, quotients int) {
	w, n := int(s.width), int(s.nslots)
	q, e := int(v>>w), v&mask(w)
	occ, re := s.occupieds(), s.runends()

	if testBit(occ, q) {
		// Grow the existing run, keeping it sorted.
		start, end := s.run(q)
		at := start + sort.Search(end-start+1, func(i int) bool {
			return compareEntries(e, s.slot(start+i)) < 0
		})
		// Shift whichever side of the run reaches an empty slot sooner.
		up := s.nextEmpty(end + 1)
		down := s.roomBelow(start-1, up, up-at-(at-start))
		clearBit(re, end)
		if down < 0 {
			s.shiftSlotsUp(at, up)
			shiftUp(re, at, up, 1)
			s.setSlot(at, e)
			setBit(re, end+1)
		} else {
			s.shiftSlotsDown(down+1, at)
			shiftDown(re, down+1, at, 1)
			s.setSlot(at-1, e)
			setBit(re, end)
		}
		s.count++
		return
	}

	// Start a new run between the runs of the neighbouring quotients.
	before := -1 // last slot of the preceding run
	if k := rank(occ, q); k > 0 {
		before = selectBit(re, k-1)
	}
	after := n // first slot of the following run
	if end := nextSet(re, before+1); end < n {
		after = s.firstUsed(before+1, end)
	}
	switch {
	case after-before > 1:
		// There is room; take the free slot closest to the mapped position.
		at := min(max(q*n/quotients, before+1), after-1)
		s.setSlot(at, e)
		setBit(re, at)
	default:
		up := s.nextEmpty(after)
		if down := s.roomBelow(before-1, up, up-after-1); down < 0 {
			s.shiftSlotsUp(after, up)
			shiftUp(re, after, up, 1)
			s.setSlot(after, e)
			setBit(re, after)
		} else {
			s.shiftSlotsDown(down+1, before+1)
			shiftDown(re, down+1, before+1, 1)
			s.setSlot(before, e)
			setBit(re, before)
		}
	}
	setBit(occ, q)
	s.count++
}

// remove deletes the most specific entry that stands for v, a full-length
// entry, and reports whether there was one.
//
// Taking the most specific match keeps deletes safe when entries of different
// lengths cover the same key: whatever is left still covers every key the
// removed entry covered.
func (s *store) remove(v uint64) bool {
	w := int(s.width)
	q, e := int(v>>w), v&mask(w)
	occ, re := s.occupieds(), s.runends()
	if !testBit(occ, q) {
		return false
	}
	start, end := s.run(q)
	// Entries covering e sort at or before it, the most specific one last.
	at := start + sort.Search(end-start+1, func(i int) bool {
		return compareEntries(e, s.slot(start+i)) < 0
	}) - 1
	for at >= start && !covers(s.slot(at), e) {
		at--
	}
	if at < start {
		return false
	}

	switch {
	case start == end:
		s.setSlot(at, 0)
		clearBit(re, end)
		clearBit(occ, q)
	case at-start <= end-at:
		// Close the gap from the front; the run now starts one slot later.
		s.shiftSlotsUp(start, at)
		s.setSlot(start, 0)
	default:
		// Close the gap from the back; the run now ends one slot earlier.
		s.shiftSlotsDown(at+1, end+1)
		s.setSlot(end, 0)
		clearBit(re, end)
		setBit(re, end-1)
	}
	s.count--
	return true
}

// covers reports whether entry e stands for the full-length entry x of the
// same quotient.
func covers(e, x uint64) bool {
	m := delimiter(e)<<1 - 1
	return e|m == x|m
}

// overlaps reports whether the run of quotient q, which must be occupied,
// has an entry standing for some remainder in [lo, hi].
func (s *store) overlaps(q int, lo, hi uint64) bool {
	re := s.runends()
	i := selectBit(re, rank(s.occupieds(), q))
	e := s.slot(i)
	for {
		if ceilOf(e) >= lo && floorOf(e) <= hi {
			return true
		}
		if i == 0 {
			return false
		}
		i--
		if testBit(re, i) {
			return false
		}
		if e = s.slot(i); e == 0 {
			return false
		}
	}
}

// mayContain reports whether some entry stands for the full-length entry v.
func (s *store) mayContain(v uint64) bool {
	w := int(s.width)
	q, e := int(v>>w), v&mask(w)
	return testBit(s.occupieds(), q) && s.overlaps(q, e, e)
}

// mayContainRange reports whether some entry stands for a full-length entry
// in [l, r]. Both bounds are full-length entries and l must not exceed r.
func (s *store) mayContainRange(l, r uint64) bool {
	w := int(s.width)
	lq, rq := int(l>>w), int(r>>w)
	lrem, rrem := l&mask(w), r&mask(w)
	occ := s.occupieds()
	if lq == rq {
		return testBit(occ, lq) && s.overlaps(lq, lrem, rrem)
	}
	if nextSet(occ, lq+1) < rq {
		return true // a whole run lies strictly inside the range
	}
	return testBit(occ, lq) && s.overlaps(lq, lrem, mask(w)) ||
		testBit(occ, rq) && s.overlaps(rq, 0, rrem)
}

// appendEntries appends the store's entries to dst in sorted order.
func (s *store) appendEntries(dst []uint64) []uint64 {
	w := int(s.width)
	occ, re := s.occupieds(), s.runends()
	dst = slices.Grow(dst, int(s.count))
	i, pos := 0, s.slotPos(0)
	for q := nextSet(occ, 0); q < storeTarget; q = nextSet(occ, q+1) {
		// The run of q is the used tail of the slots up to its run end.
		for end := nextSet(re, i); i <= end; i, pos = i+1, pos+w {
			if e := getBits(s.words, pos, w); e != 0 {
				dst = append(dst, uint64(q)<<w|e)
			}
		}
	}
	return dst
}

// load fills an empty store with entries, which must be sorted and number at
// most nslots. Each run starts at the slot its quotient maps to, or right
// after the previous run if that slot is taken, but never so late that the
// entries still to come would not fit behind it.
func (s *store) load(entries []uint64, quotients int) {
	s.count = int32(len(entries))
	w, n := int(s.width), int(s.nslots)
	occ, re := s.occupieds(), s.runends()

	scale := n << 20 / quotients // maps a quotient to a slot without dividing
	at := 0                      // first slot after the runs placed so far
	for i := 0; i < len(entries); {
		q := int(entries[i] >> w)
		at = min(max(at, q*scale>>20), n-(len(entries)-i))
		setBit(occ, q)
		for pos := s.slotPos(at); i < len(entries) && int(entries[i]>>w) == q; pos += w {
			setBits(s.words, pos, w, entries[i])
			i++
			at++
		}
		setBit(re, at-1)
	}
}
