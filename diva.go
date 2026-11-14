package diva

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"slices"
	"unsafe"
)

// ErrUnsorted is returned by [Build] when its input is not in ascending order.
var ErrUnsorted = errors.New("diva: keys are not in ascending order")

const (
	defaultFingerprintBits = 8
	defaultLoadFactor      = 0.95
	defaultSeed            = 0x44495641 // "DIVA"

	// A split is abandoned when recoding would multiply the entries of the
	// store beyond this many times its size, plus storeTarget.
	maxSplitGrowth = 2
)

type config struct {
	fingerprintBits int
	loadFactor      float64
	seed            uint64
}

// An Option configures a [Filter].
type Option func(*config)

// WithFingerprintBits sets how many key bits a filter keeps per key on top of
// the roughly ten bits that its position in an infix store encodes. More bits
// mean fewer false positives: a filter built from sorted keys answers a point
// query for an absent key with a false positive about once in 2^(n-1) tries.
// Each key costs about n+2 bits divided by the load factor, plus two.
//
// n must be between 1 and 32. The default is 8.
func WithFingerprintBits(n int) Option {
	return func(c *config) { c.fingerprintBits = n }
}

// WithLoadFactor sets how full an infix store may get before it grows. A
// higher load factor saves memory and makes inserts and deletes slower.
//
// The load factor must be between 0.5 and 0.99. The default is 0.95.
func WithLoadFactor(load float64) Option {
	return func(c *config) { c.loadFactor = load }
}

// WithSeed seeds the generator that decides which inserted keys become
// samples. Filters with the same seed that see the same operations are
// identical. The default is a fixed seed.
func WithSeed(seed uint64) Option {
	return func(c *config) { c.seed = seed }
}

// A Filter is a Diva range filter: an approximate, updatable set of byte
// string keys that answers point and range emptiness queries with no false
// negatives and a bounded rate of false positives.
//
// Keys are compared as byte strings. Trailing zero bytes are not significant:
// "a" and "a\x00" are the same key. The filter is a multiset, so a key
// inserted twice must be deleted twice.
//
// A Filter is not safe for concurrent use while it is being modified. Any
// number of goroutines may query it concurrently between modifications.
//
// The zero value is not usable; create filters with [New] or [Build].
type Filter struct {
	// samples are the keys the filter stores in full, in ascending order.
	// Each but the last owns the infix store of the keys up to the next one.
	// heads[i] is the first eight bytes of samples[i].key as a big-endian
	// integer, so that most of a search stays within one compact slice.
	samples []sample
	heads   []uint64
	// below holds the keys smaller than every sample and above the keys at
	// or beyond the last one, both in full and sorted. Once one of them has
	// enough keys it becomes an infix store with a new sample at its far end,
	// so the sampled range grows to follow the data.
	below, above [][]byte

	keys  int
	width int // slot width: fingerprint bits plus the delimiter
	load  float64

	// One inserted key in period becomes a sample, and a buffer is flushed
	// once it holds burst keys. Both are storeTarget outside of tests.
	period  uint64
	burst   int
	rng     rand.PCG
	scratch []uint64
}

type sample struct {
	key   []byte
	store *store // keys in [key, next sample); nil for the last sample
	span  span   // how those keys map to entries; unset for the last sample
	// present is false for a sample whose key has been deleted but which
	// still has to mark the edge of a store.
	present bool
}

// New returns an empty filter.
func New(opts ...Option) (*Filter, error) {
	c := config{
		fingerprintBits: defaultFingerprintBits,
		loadFactor:      defaultLoadFactor,
		seed:            defaultSeed,
	}
	for _, opt := range opts {
		opt(&c)
	}
	if c.fingerprintBits < 1 || c.fingerprintBits > 32 {
		return nil, fmt.Errorf("diva: fingerprint bits must be between 1 and 32, got %d", c.fingerprintBits)
	}
	if !(c.loadFactor >= 0.5 && c.loadFactor <= 0.99) {
		return nil, fmt.Errorf("diva: load factor must be between 0.5 and 0.99, got %v", c.loadFactor)
	}
	return &Filter{
		width:  c.fingerprintBits + 1,
		load:   c.loadFactor,
		period: storeTarget,
		burst:  storeTarget,
		rng:    *rand.NewPCG(c.seed, c.seed),
	}, nil
}

// Build returns a filter holding keys, which must arrive in ascending order;
// repeated keys are fine. It returns [ErrUnsorted] otherwise.
//
// Building from sorted keys is faster than inserting them one by one and
// gives the most accurate filter, because every infix store is derived from
// the samples that will surround it.
func Build(keys iter.Seq[[]byte], opts ...Option) (*Filter, error) {
	f, err := New(opts...)
	if err != nil {
		return nil, err
	}
	// Keys wait in f.above until the sample that ends their store arrives.
	// They are packed into one arena that is reused after every flush, which
	// spares an allocation per key.
	var arena, last []byte
	for key := range keys {
		k := canon(key)
		if f.keys > 0 && bytes.Compare(k, last) < 0 {
			return nil, ErrUnsorted
		}
		last = append(last[:0], k...)
		f.keys++
		if len(f.samples) == 0 {
			f.insertSample(0, sample{key: bytes.Clone(k), present: true})
			continue
		}
		arena = append(arena, k...)
		f.above = append(f.above, arena[len(arena)-len(k):len(arena):len(arena)])
		if len(f.above) >= f.burst && f.flushAbove() {
			f.ownAbove()
			arena = arena[:0]
		}
	}
	f.flushAbove()
	f.ownAbove()
	return f, nil
}

// ownAbove gives the keys in f.above storage of their own.
func (f *Filter) ownAbove() {
	for i, k := range f.above {
		f.above[i] = bytes.Clone(k)
	}
}

// Len returns the number of keys in the filter, counting repeats.
func (f *Filter) Len() int { return f.keys }

// SizeBytes returns the approximate amount of memory the filter occupies.
func (f *Filter) SizeBytes() int {
	const sliceHeader = int(unsafe.Sizeof([]byte(nil)))
	size := int(unsafe.Sizeof(*f))
	for i := range f.samples {
		s := &f.samples[i]
		size += int(unsafe.Sizeof(*s)) + 8 + len(s.key)
		if s.store != nil {
			size += int(unsafe.Sizeof(*s.store)) + 8*len(s.store.words)
		}
	}
	for _, k := range f.below {
		size += sliceHeader + len(k)
	}
	for _, k := range f.above {
		size += sliceHeader + len(k)
	}
	return size
}

// Insert adds key to the filter.
func (f *Filter) Insert(key []byte) {
	k := canon(key)
	f.keys++
	if len(f.samples) == 0 {
		f.insertSample(0, sample{key: bytes.Clone(k), present: true})
		return
	}
	i := f.find(k)
	if i < 0 {
		f.below = insertKey(f.below, k)
		if len(f.below) >= f.burst {
			f.flushBelow()
		}
		return
	}
	s := &f.samples[i]
	isSample := bytes.Equal(s.key, k)
	if isSample && !s.present {
		s.present = true
		return
	}
	if i == len(f.samples)-1 {
		f.above = insertKey(f.above, k)
		if len(f.above) >= f.burst {
			f.flushAbove()
		}
		return
	}
	e := s.span.entry(k, f.width)
	// Promoting one key in storeTarget to a sample keeps the stores near
	// their target size as the filter grows.
	if f.rng.Uint64()%f.period == 0 && !isSample && f.split(i, k, e) {
		return
	}
	if st := s.store; int(st.count) >= int(f.load*float64(st.nslots)) {
		grown := int(math.Ceil(float64(st.nslots) / f.load))
		f.resize(s, max(grown, int(st.nslots)+1))
	}
	s.store.insert(e, int(s.span.quotients))
}

// Delete removes one occurrence of key and reports whether the filter had an
// entry for it.
//
// Only delete keys that are in the filter. Like any filter that supports
// deletion, Diva cannot tell a key from another one that maps to the same
// entry, so deleting an absent key may remove the entry of a present one and
// make later queries for that key return false.
func (f *Filter) Delete(key []byte) bool {
	k := canon(key)
	i := f.find(k)
	var ok bool
	switch {
	case i < 0:
		f.below, ok = removeKey(f.below, k)
	case i == len(f.samples)-1:
		if f.above, ok = removeKey(f.above, k); !ok {
			if s := &f.samples[i]; s.present && bytes.Equal(s.key, k) {
				f.unsample(i)
				ok = true
			}
		}
	default:
		s := &f.samples[i]
		if s.present && bytes.Equal(s.key, k) {
			f.unsample(i)
			ok = true
			break
		}
		if ok = s.store.remove(s.span.entry(k, f.width)); !ok {
			break
		}
		st := s.store
		if n := float64(st.nslots); st.nslots > minSlots && float64(st.count) < f.load*f.load*f.load*n {
			f.resize(s, max(minSlots, int(math.Ceil(float64(st.count)/(f.load*f.load)))))
		}
	}
	if !ok {
		return false
	}
	f.keys--
	f.trim()
	return true
}

// MayContain reports whether key may be in the filter. A false result is
// definite; a true result may be a false positive.
func (f *Filter) MayContain(key []byte) bool {
	k := canon(key)
	i := f.find(k)
	if i < 0 {
		return containsKey(f.below, k)
	}
	s := &f.samples[i]
	if s.present && bytes.Equal(s.key, k) {
		return true
	}
	if i == len(f.samples)-1 {
		return containsKey(f.above, k)
	}
	return s.store.mayContain(s.span.entry(k, f.width))
}

// MayContainRange reports whether the filter may hold a key k with
// lo <= k <= hi. A false result is definite; a true result may be a false
// positive. A range with lo > hi is empty.
func (f *Filter) MayContainRange(lo, hi []byte) bool {
	lo, hi = canon(lo), canon(hi)
	if bytes.Compare(lo, hi) > 0 {
		return false
	}
	if intersects(f.below, lo, hi) || intersects(f.above, lo, hi) {
		return true
	}
	// Walk the samples from the one whose store holds lo. The walk ends at
	// the first present sample in the range, so it is short in practice.
	last := len(f.samples) - 1
	for i := max(f.find(lo), 0); i <= last; i++ {
		s := &f.samples[i]
		if bytes.Compare(s.key, hi) > 0 {
			return false
		}
		fromStart := bytes.Compare(lo, s.key) <= 0
		if s.present && fromStart {
			return true
		}
		if i == last || s.store.count == 0 {
			continue
		}
		l, r := uint64(1), s.span.lastEntry(f.width)
		if !fromStart {
			l = s.span.entry(lo, f.width)
		}
		if bytes.Compare(hi, f.samples[i+1].key) < 0 {
			r = s.span.entry(hi, f.width)
		}
		if s.store.mayContainRange(l, r) {
			return true
		}
	}
	return false
}

// find returns the index of the last sample that is not larger than k, or -1
// if there is none.
func (f *Filter) find(k []byte) int {
	// Samples whose first eight bytes differ from k's are ordered by them.
	head := wordAt(k, 0)
	lo, found := slices.BinarySearch(f.heads, head)
	if !found {
		return lo - 1
	}
	hi := len(f.heads)
	if head != math.MaxUint64 {
		n, _ := slices.BinarySearch(f.heads[lo:], head+1)
		hi = lo + n
	}
	// The rest share those bytes with k and are ordered by what follows.
	i, found := slices.BinarySearchFunc(f.samples[lo:hi], tail(k), func(s sample, t []byte) int {
		return bytes.Compare(tail(s.key), t)
	})
	if found {
		return lo + i
	}
	return lo + i - 1
}

// tail returns what follows the first eight bytes of k.
func tail(k []byte) []byte { return k[min(8, len(k)):] }

func (f *Filter) insertSample(i int, s sample) {
	f.samples = slices.Insert(f.samples, i, s)
	f.heads = slices.Insert(f.heads, i, wordAt(s.key, 0))
}

func (f *Filter) removeSample(i int) {
	f.samples = slices.Delete(f.samples, i, i+1)
	f.heads = slices.Delete(f.heads, i, i+1)
}

// build returns a store holding entries, which must be sorted.
func (f *Filter) build(entries []uint64, sp span) *store {
	slots := max(minSlots, int(math.Ceil(float64(len(entries))/f.load)))
	st := newStore(f.width, slots)
	st.load(entries, int(sp.quotients))
	return st
}

// resize moves the store of s into one with the given number of slots.
func (f *Filter) resize(s *sample, slots int) {
	f.scratch = s.store.appendEntries(f.scratch[:0])
	st := newStore(f.width, slots)
	st.load(f.scratch, int(s.span.quotients))
	s.store = st
}

// recodeStore appends entries, recoded for span to, to dst.
func (f *Filter) recodeStore(dst, entries []uint64, from origin, to span) []uint64 {
	for _, e := range entries {
		dst = recode(dst, e, from, to, f.width)
	}
	return dst
}

// split makes k, whose entry in the store of sample i is e, a sample of its
// own and divides the store around it. It reports whether it did.
//
// A split is declined when an existing entry stands for e, because the key
// behind that entry could then lie on either side of k, and when the entries
// have too little resolution left to be divided without multiplying.
func (f *Filter) split(i int, k []byte, e uint64) bool {
	s := &f.samples[i]
	if s.store.mayContain(e) {
		return false
	}
	f.scratch = s.store.appendEntries(f.scratch[:0])
	entries := f.scratch
	// No entry stands for e, so each one lies wholly before or after it.
	cut, _ := slices.BinarySearchFunc(entries, e, func(x, e uint64) int {
		if floorOf(x) > e {
			return 1
		}
		return -1
	})

	from := origin{span: s.span, prev: s.key}
	left, right := newSpan(s.key, k), newSpan(k, f.samples[i+1].key)
	budget := maxSplitGrowth*len(entries) + storeTarget
	out := f.recodeStore(make([]uint64, 0, len(entries)), entries[:cut], from, left)
	mid := len(out)
	out = f.recodeStore(out, entries[cut:], from, right)
	if len(out) > budget {
		return false
	}
	slices.SortFunc(out[:mid], compareEntries)
	slices.SortFunc(out[mid:], compareEntries)

	s.span, s.store = left, f.build(out[:mid], left)
	f.insertSample(i+1, sample{
		key:     bytes.Clone(k),
		store:   f.build(out[mid:], right),
		span:    right,
		present: true,
	})
	return true
}

// merge removes sample i, which must have a sample on either side, and folds
// the two stores it separated into one.
func (f *Filter) merge(i int) {
	l, m, r := &f.samples[i-1], &f.samples[i], &f.samples[i+1]
	to := newSpan(l.key, r.key)
	out := make([]uint64, 0, int(l.store.count+m.store.count))
	for _, s := range []*sample{l, m} {
		f.scratch = s.store.appendEntries(f.scratch[:0])
		out = f.recodeStore(out, f.scratch, origin{span: s.span, prev: s.key}, to)
	}
	slices.SortFunc(out, compareEntries)
	l.span, l.store = to, f.build(out, to)
	f.removeSample(i)
}

// unsample takes the key of sample i, which must be present, out of the set.
// An interior sample is removed and the stores on either side of it merge.
// The first and last sample have a store on one side only, so they stay as
// boundaries that are no longer keys until trim finds them unused.
func (f *Filter) unsample(i int) {
	if 0 < i && i < len(f.samples)-1 {
		f.merge(i)
		return
	}
	f.samples[i].present = false
}

// trim drops deleted samples at either end that no longer bound anything.
func (f *Filter) trim() {
	for len(f.samples) > 1 && !f.samples[0].present && f.samples[0].store.count == 0 {
		f.removeSample(0)
	}
	for n := len(f.samples); n > 1 && !f.samples[n-1].present && f.samples[n-2].store.count == 0; n-- {
		f.removeSample(n - 1)
		f.samples[n-2].store, f.samples[n-2].span = nil, span{}
	}
	// A lone deleted sample only keeps below and above apart.
	if len(f.samples) == 1 && !f.samples[0].present && len(f.below)+len(f.above) == 0 {
		f.removeSample(0)
	}
}

// flushAbove turns the keys buffered beyond the last sample into an infix
// store, bounded by the largest of them as the new last sample. It reports
// whether it did, which takes a key larger than the last sample.
func (f *Filter) flushAbove() bool {
	if len(f.above) == 0 {
		return false
	}
	last := len(f.samples) - 1
	s := &f.samples[last]
	top := f.above[len(f.above)-1]
	if bytes.Equal(top, s.key) {
		return false // nothing but repeats of the last sample
	}
	// Repeats of the new sample are not below it, so they stay buffered.
	n, _ := slices.BinarySearchFunc(f.above, top, bytes.Compare)
	s.span = newSpan(s.key, top)
	entries := f.scratch[:0]
	for _, k := range f.above[:n] {
		entries = append(entries, s.span.entry(k, f.width))
	}
	f.scratch = entries
	s.store = f.build(entries, s.span)
	f.insertSample(last+1, sample{key: bytes.Clone(top), present: true})
	f.above = slices.Delete(f.above, 0, n+1)
	if last > 0 && !f.samples[last].present {
		f.merge(last) // a deleted sample no longer has to mark the edge
	}
	return true
}

// flushBelow turns the keys buffered before the first sample into an infix
// store, bounded by the smallest of them as the new first sample.
func (f *Filter) flushBelow() {
	first := f.below[0]
	sp := newSpan(first, f.samples[0].key)
	entries := f.scratch[:0]
	for _, k := range f.below[1:] {
		entries = append(entries, sp.entry(k, f.width))
	}
	f.scratch = entries
	f.insertSample(0, sample{
		key:     first,
		store:   f.build(entries, sp),
		span:    sp,
		present: true,
	})
	clear(f.below)
	f.below = f.below[:0]
	if len(f.samples) > 2 && !f.samples[1].present {
		f.merge(1) // a deleted sample no longer has to mark the edge
	}
}

// insertKey inserts a copy of k into the sorted slice keys.
func insertKey(keys [][]byte, k []byte) [][]byte {
	i := len(keys)
	if i > 0 && bytes.Compare(keys[i-1], k) > 0 {
		i, _ = slices.BinarySearchFunc(keys, k, bytes.Compare)
	}
	return slices.Insert(keys, i, bytes.Clone(k))
}

// removeKey removes one occurrence of k from the sorted slice keys.
func removeKey(keys [][]byte, k []byte) ([][]byte, bool) {
	i, found := slices.BinarySearchFunc(keys, k, bytes.Compare)
	if !found {
		return keys, false
	}
	return slices.Delete(keys, i, i+1), true
}

func containsKey(keys [][]byte, k []byte) bool {
	_, found := slices.BinarySearchFunc(keys, k, bytes.Compare)
	return found
}

// intersects reports whether the sorted slice keys has a key in [lo, hi].
func intersects(keys [][]byte, lo, hi []byte) bool {
	i, _ := slices.BinarySearchFunc(keys, lo, bytes.Compare)
	return i < len(keys) && bytes.Compare(keys[i], hi) <= 0
}
