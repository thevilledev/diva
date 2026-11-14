package diva

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// storeModel is the obvious implementation an infix store is checked
// against: a sorted slice of entries.
type storeModel struct {
	width   int
	entries []uint64
}

func (m *storeModel) insert(v uint64) {
	i, _ := slices.BinarySearchFunc(m.entries, v, func(x, v uint64) int {
		if compareEntries(x, v) <= 0 {
			return -1
		}
		return 1
	})
	m.entries = slices.Insert(m.entries, i, v)
}

// covering returns the index of the most specific entry standing for the
// full-length entry v, or -1.
func (m *storeModel) covering(v uint64) int {
	for i, e := range slices.Backward(m.entries) {
		if e>>m.width == v>>m.width && covers(e, v) {
			return i
		}
	}
	return -1
}

func (m *storeModel) overlaps(l, r uint64) bool {
	for _, e := range m.entries {
		if ceilOf(e) >= l && floorOf(e) <= r {
			return true
		}
	}
	return false
}

// randomEntry returns an entry with a random quotient. About one in four is
// shorter than full length.
func randomEntry(r *rand.Rand, width, quotients int) uint64 {
	e := r.Uint64()&mask(width) | 1
	if r.IntN(4) == 0 {
		t := r.IntN(width)
		e = e&^mask(t) | 1<<t
	}
	return uint64(r.IntN(quotients))<<width | e
}

func randomFull(r *rand.Rand, width, quotients int) uint64 {
	return uint64(r.IntN(quotients))<<width | r.Uint64()&mask(width) | 1
}

func checkStore(t *testing.T, s *store, m *storeModel, quotients int) {
	t.Helper()
	if err := s.check(quotients); err != nil {
		t.Fatal(err)
	}
	if got := s.appendEntries(nil); !slices.Equal(got, m.entries) {
		t.Fatalf("store holds %d entries that differ from the model's %d", len(got), len(m.entries))
	}
}

func TestEntryOrder(t *testing.T) {
	// Four-bit remainders in order: a prefix sorts before everything it stands for.
	sorted := []uint64{0b1000, 0b0100, 0b0010, 0b0001, 0b0011, 0b0110, 0b0101, 0b1100, 0b1111}
	for i := 1; i < len(sorted); i++ {
		if compareEntries(sorted[i-1], sorted[i]) >= 0 {
			t.Errorf("%04b should sort before %04b", sorted[i-1], sorted[i])
		}
	}
	if floorOf(0b0110) != 0b0100 || ceilOf(0b0110) != 0b0111 {
		t.Errorf("0110 stands for [%04b, %04b]", floorOf(0b0110), ceilOf(0b0110))
	}
	for _, c := range []struct {
		e, x uint64
		want bool
	}{
		{0b0101, 0b0101, true},
		{0b0101, 0b0111, false},
		{0b0110, 0b0101, true},
		{0b0110, 0b0111, true},
		{0b0110, 0b0011, false},
		{0b1000, 0b0001, true},
		{0b1000, 0b1111, true},
	} {
		if got := covers(c.e, c.x); got != c.want {
			t.Errorf("covers(%04b, %04b) = %v, want %v", c.e, c.x, got, c.want)
		}
	}
}

func TestStoreEmpty(t *testing.T) {
	s := newStore(9, minSlots)
	if err := s.check(storeTarget); err != nil {
		t.Fatal(err)
	}
	if s.mayContain(5<<9|1) || s.mayContainRange(1, storeTarget<<9-1) || s.remove(5<<9|1) {
		t.Error("empty store reports an entry")
	}
	if got := s.appendEntries(nil); len(got) != 0 {
		t.Errorf("empty store lists %d entries", len(got))
	}
	s.load(nil, storeTarget)
	if err := s.check(storeTarget); err != nil {
		t.Fatal(err)
	}
}

// TestStoreFill inserts into a store until it is one slot short of full,
// which forces shifts in both directions and across every word boundary.
func TestStoreFill(t *testing.T) {
	for _, width := range []int{2, 5, 9, 17, 33} {
		for _, quotients := range []int{storeTarget / 2, 777, storeTarget} {
			r := rand.New(rand.NewPCG(uint64(width), uint64(quotients)))
			const slots = 300
			s := newStore(width, slots)
			m := &storeModel{width: width}
			for range slots - 1 {
				v := randomEntry(r, width, quotients)
				s.insert(v, quotients)
				m.insert(v)
			}
			checkStore(t, s, m, quotients)
			for len(m.entries) > 0 {
				v := m.entries[r.IntN(len(m.entries))]
				if !s.remove(ceilOf(v)) {
					t.Fatalf("width %d: remove(%#x) found nothing", width, v)
				}
				i := m.covering(ceilOf(v))
				m.entries = slices.Delete(m.entries, i, i+1)
				if len(m.entries)%37 == 0 {
					checkStore(t, s, m, quotients)
				}
			}
			checkStore(t, s, m, quotients)
		}
	}
}

// TestStoreRandom runs a random mix of operations against the model.
func TestStoreRandom(t *testing.T) {
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 7))
		width := 2 + r.IntN(12)
		quotients := storeTarget/2 + r.IntN(storeTarget/2+1)
		if seed%4 == 0 {
			quotients = 3 // long runs
		}
		slots := 8 + r.IntN(200)
		s := newStore(width, slots)
		m := &storeModel{width: width}

		for op := range 3000 {
			switch c := r.IntN(10); {
			case c < 5 && len(m.entries) < slots-1:
				v := randomEntry(r, width, quotients)
				s.insert(v, quotients)
				m.insert(v)
			case c < 8:
				v := randomFull(r, width, quotients)
				if len(m.entries) > 0 && r.IntN(3) > 0 {
					v = ceilOf(m.entries[r.IntN(len(m.entries))])
				}
				i := m.covering(v)
				if got := s.remove(v); got != (i >= 0) {
					t.Fatalf("seed %d op %d: remove(%#x) = %v, want %v", seed, op, v, got, i >= 0)
				}
				if i >= 0 {
					m.entries = slices.Delete(m.entries, i, i+1)
				}
			default:
				v := randomFull(r, width, quotients)
				if got, want := s.mayContain(v), m.covering(v) >= 0; got != want {
					t.Fatalf("seed %d op %d: mayContain(%#x) = %v, want %v", seed, op, v, got, want)
				}
				l, h := randomFull(r, width, quotients), randomFull(r, width, quotients)
				if r.IntN(2) == 0 {
					h = l + uint64(r.IntN(8))<<1 // a short range
					h = min(h, uint64(quotients)<<width-1)
				}
				if l > h {
					l, h = h, l
				}
				if got, want := s.mayContainRange(l, h), m.overlaps(l, h); got != want {
					t.Fatalf("seed %d op %d: mayContainRange(%#x, %#x) = %v, want %v", seed, op, l, h, got, want)
				}
			}
			if op%97 == 0 {
				checkStore(t, s, m, quotients)
			}
		}
		checkStore(t, s, m, quotients)
	}
}

// TestStoreLoad checks that loading a sorted list gives the same store
// contents as inserting its entries one by one, at any fill level.
func TestStoreLoad(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 13))
	for range 200 {
		width := 2 + r.IntN(20)
		quotients := storeTarget/2 + r.IntN(storeTarget/2+1)
		slots := 8 + r.IntN(1200)
		m := &storeModel{width: width}
		for range r.IntN(slots + 1) {
			m.insert(randomEntry(r, width, quotients))
		}
		if r.IntN(5) == 0 { // everything crowds towards the end of the array
			for i, e := range m.entries {
				q := quotients - 1 - int(e>>width)%3
				m.entries[i] = uint64(q)<<width | e&mask(width)
			}
			slices.SortFunc(m.entries, compareEntries)
		}
		s := newStore(width, slots)
		s.load(m.entries, quotients)
		checkStore(t, s, m, quotients)
		for _, e := range m.entries {
			if !s.mayContain(ceilOf(e)) {
				t.Fatalf("loaded store misses %#x", e)
			}
		}
	}
}

// TestStoreRemoveMostSpecific pins down which of several covering entries a
// delete takes.
func TestStoreRemoveMostSpecific(t *testing.T) {
	const w = 4
	s := newStore(w, 16)
	for _, e := range []uint64{0b1000, 0b0100, 0b0101, 0b0111} {
		s.insert(7<<w|e, storeTarget)
	}
	for _, step := range []struct {
		remove uint64
		want   []uint64
	}{
		{0b0101, []uint64{0b1000, 0b0100, 0b0111}},
		{0b0101, []uint64{0b1000, 0b0111}},
		{0b0101, []uint64{0b0111}},
	} {
		if !s.remove(7<<w | step.remove) {
			t.Fatalf("remove(%04b) found nothing", step.remove)
		}
		var got []uint64
		for _, e := range s.appendEntries(nil) {
			got = append(got, e&mask(w))
		}
		if !slices.Equal(got, step.want) {
			t.Fatalf("after remove(%04b): %04b, want %04b", step.remove, got, step.want)
		}
	}
	if s.remove(7<<w | 0b0101) {
		t.Error("remove found an entry that does not stand for the key")
	}
	if s.remove(6<<w | 0b0111) {
		t.Error("remove found an entry of another quotient")
	}
}

// TestEmptySlots checks the searches for an empty slot against a scan that
// looks at one slot at a time.
func TestEmptySlots(t *testing.T) {
	for _, width := range []int{2, 5, 9, 17, 32, 33} {
		r := rand.New(rand.NewPCG(uint64(width), 6))
		for range 60 {
			n := 1 + r.IntN(150)
			s := newStore(width, n)
			used := make([]bool, n)
			density := r.Float64()
			for i := range n {
				if used[i] = r.Float64() < density; !used[i] {
					continue
				}
				// Any non-zero value, among them a lone top or bottom bit.
				s.setSlot(i, r.Uint64()&mask(width)&-uint64(r.IntN(2))|1<<r.IntN(width))
			}

			for i := range n + 1 {
				want := i
				for want < n && used[want] {
					want++
				}
				if got := s.nextEmpty(i); got != want {
					t.Fatalf("width %d, %d slots: nextEmpty(%d) = %d, want %d", width, n, i, got, want)
				}
			}
			for i := -1; i < n; i++ {
				floor := i - r.IntN(i+2)
				want := i
				for want > floor && used[want] {
					want--
				}
				if want == floor {
					want = -1
				}
				if got := s.emptyBelow(i, floor); got != want {
					t.Fatalf("width %d, %d slots: emptyBelow(%d, %d) = %d, want %d", width, n, i, floor, got, want)
				}
			}
		}
	}
}
