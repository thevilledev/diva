package diva

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// oracle is the exact multiset a filter is checked against.
type oracle struct {
	counts map[string]int
	all    []string // one element per inserted key, in no particular order
	sorted []string // distinct keys in order
}

func newOracle() *oracle { return &oracle{counts: map[string]int{}} }

func (o *oracle) add(k []byte) {
	c := string(canon(k))
	o.all = append(o.all, c)
	o.counts[c]++
	if o.counts[c] == 1 {
		i, _ := slices.BinarySearch(o.sorted, c)
		o.sorted = slices.Insert(o.sorted, i, c)
	}
}

// removeAt removes the i-th element of all.
func (o *oracle) removeAt(i int) []byte {
	c := o.all[i]
	o.all[i] = o.all[len(o.all)-1]
	o.all = o.all[:len(o.all)-1]
	o.counts[c]--
	if o.counts[c] == 0 {
		delete(o.counts, c)
		j, _ := slices.BinarySearch(o.sorted, c)
		o.sorted = slices.Delete(o.sorted, j, j+1)
	}
	return []byte(c)
}

// take removes and returns a random key.
func (o *oracle) take(r *rand.Rand) []byte { return o.removeAt(r.IntN(len(o.all))) }

// remove removes one occurrence of k, which must be present.
func (o *oracle) remove(k []byte) { o.removeAt(slices.Index(o.all, string(canon(k)))) }

func (o *oracle) keys() []string { return o.sorted }

func (o *oracle) has(k []byte) bool { return o.counts[string(canon(k))] > 0 }

func (o *oracle) hasRange(lo, hi []byte) bool {
	i, _ := slices.BinarySearch(o.sorted, string(canon(lo)))
	return i < len(o.sorted) && o.sorted[i] <= string(canon(hi))
}

// verify checks the filter's invariants and that it has no false negatives
// for any key of the oracle, as a point or inside a range.
func verify(t *testing.T, f *Filter, o *oracle) {
	t.Helper()
	if err := f.check(); err != nil {
		t.Fatal(err)
	}
	if f.Len() != len(o.all) {
		t.Fatalf("Len() = %d, want %d", f.Len(), len(o.all))
	}
	keys := o.keys()
	for i, k := range keys {
		key := []byte(k)
		if !f.MayContain(key) {
			t.Fatalf("false negative for key %x", key)
		}
		if !f.MayContainRange(key, key) {
			t.Fatalf("false negative for range [%x, %x]", key, key)
		}
		// Ranges that reach the key from either side, and one that only
		// contains it because trailing zero bytes do not count.
		padded := append(bytes.Clone(key), 0, 0)
		if !f.MayContain(padded) || !f.MayContainRange(padded, append(bytes.Clone(key), 0, 0, 1)) {
			t.Fatalf("false negative for zero-padded key %x", key)
		}
		if i > 0 {
			if lo := append([]byte(keys[i-1]), 0, 1); bytes.Compare(lo, key) <= 0 && !f.MayContainRange(lo, key) {
				t.Fatalf("false negative for range [%x, %x]", lo, key)
			}
		}
		if i+1 < len(keys) && keys[i+1] != "" {
			if hi := []byte(keys[i+1][:len(keys[i+1])-1]); bytes.Compare(key, hi) <= 0 && !f.MayContainRange(key, hi) {
				t.Fatalf("false negative for range [%x, %x]", key, hi)
			}
		}
	}
	if len(keys) > 0 && !f.MayContainRange(nil, bytes.Repeat([]byte{0xff}, 64)) {
		t.Fatal("false negative for the range of all keys")
	}
}

// Key generators for the workloads below.

func uniformKey(r *rand.Rand) []byte {
	return binary.BigEndian.AppendUint64(nil, r.Uint64())
}

func smallIntKey(r *rand.Rand) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(r.IntN(5000)))
}

func stringKey(r *rand.Rand) []byte {
	return fmt.Appendf(nil, "user:%06d", r.IntN(20000))
}

func clusteredKey(r *rand.Rand) []byte {
	c := byte(r.IntN(8))
	return []byte{c * 37, c, 0x40, byte(r.Uint32()), byte(r.IntN(4))}
}

func repeatedKey(r *rand.Rand) []byte {
	return []byte{byte(r.IntN(6)), byte(r.IntN(8))}
}

var workloads = []struct {
	name string
	gen  func(*rand.Rand) []byte
}{
	{"uniform", uniformKey},
	{"small-ints", smallIntKey},
	{"strings", stringKey},
	{"edgy", edgyKey},
	{"clustered", clusteredKey},
	{"repeated", repeatedKey},
}

// churn runs a random mix of inserts, deletes and queries against the filter
// and the oracle, verifying both every so often.
func churn(t *testing.T, f *Filter, o *oracle, r *rand.Rand, gen func(*rand.Rand) []byte, steps, verifyEvery int) {
	t.Helper()
	for step := range steps {
		switch c := r.IntN(100); {
		case c < 55:
			k := gen(r)
			f.Insert(k)
			o.add(k)
		case c < 85:
			if len(o.all) == 0 {
				continue
			}
			if k := o.take(r); !f.Delete(k) {
				t.Fatalf("step %d: Delete(%x) found nothing", step, k)
			}
		default:
			if k := gen(r); o.has(k) && !f.MayContain(k) {
				t.Fatalf("step %d: false negative for key %x", step, k)
			}
			lo, hi := gen(r), gen(r)
			if bytes.Compare(lo, hi) > 0 {
				lo, hi = hi, lo
			}
			if o.hasRange(lo, hi) && !f.MayContainRange(lo, hi) {
				t.Fatalf("step %d: false negative for range [%x, %x]", step, lo, hi)
			}
		}
		if step%verifyEvery == 0 {
			verify(t, f, o)
		}
	}
	verify(t, f, o)
}

func mustNew(tb testing.TB, opts ...Option) *Filter {
	tb.Helper()
	f, err := New(opts...)
	if err != nil {
		tb.Fatal(err)
	}
	return f
}

// TestChurnSmallStores shrinks the sampling period and the buffers so that a
// short run goes through thousands of flushes, splits and merges.
func TestChurnSmallStores(t *testing.T) {
	for _, w := range workloads {
		for _, bits := range []int{1, 4, 8} {
			t.Run(fmt.Sprintf("%s/%d-bits", w.name, bits), func(t *testing.T) {
				r := rand.New(rand.NewPCG(uint64(bits), 99))
				f := mustNew(t, WithFingerprintBits(bits), WithSeed(uint64(bits)))
				f.period, f.burst = 3, 6
				o := newOracle()
				churn(t, f, o, r, w.gen, 6000, 150)

				// Empty the filter, then use it again.
				for len(o.all) > 0 {
					if k := o.take(r); !f.Delete(k) {
						t.Fatalf("Delete(%x) found nothing", k)
					}
				}
				verify(t, f, o)
				churn(t, f, o, r, w.gen, 1500, 150)
			})
		}
	}
}

// TestChurn runs the same mix with the real store size.
func TestChurn(t *testing.T) {
	steps := 60000
	if testing.Short() {
		steps = 12000
	}
	for _, w := range workloads {
		t.Run(w.name, func(t *testing.T) {
			r := rand.New(rand.NewPCG(1, 2))
			f := mustNew(t)
			f.period = 64 // sample often enough to split within the test
			o := newOracle()
			churn(t, f, o, r, w.gen, steps, steps/6)
		})
	}
}

func TestBuild(t *testing.T) {
	for _, w := range workloads {
		for _, n := range []int{0, 1, 2, 1023, 1024, 1025, 5000} {
			t.Run(fmt.Sprintf("%s/%d", w.name, n), func(t *testing.T) {
				r := rand.New(rand.NewPCG(uint64(n), 3))
				keys := make([][]byte, n)
				for i := range keys {
					keys[i] = w.gen(r)
				}
				slices.SortFunc(keys, bytes.Compare)
				f, err := Build(slices.Values(keys))
				if err != nil {
					t.Fatal(err)
				}
				o := newOracle()
				for _, k := range keys {
					o.add(k)
				}
				verify(t, f, o)
				if n > 0 && len(f.below) != 0 {
					t.Errorf("Build left %d keys below the first sample", len(f.below))
				}
				// A built filter keeps working as a dynamic one.
				churn(t, f, o, r, w.gen, 3000, 1000)
			})
		}
	}
}

func TestBuildUnsorted(t *testing.T) {
	keys := [][]byte{[]byte("b"), []byte("c"), []byte("a")}
	if _, err := Build(slices.Values(keys)); !errors.Is(err, ErrUnsorted) {
		t.Errorf("Build(unsorted) error = %v, want ErrUnsorted", err)
	}
	// Keys that differ only in trailing zeros are equal, not out of order.
	keys = [][]byte{[]byte("a\x00\x00"), []byte("a"), []byte("a\x00")}
	f, err := Build(slices.Values(keys))
	if err != nil {
		t.Fatal(err)
	}
	if f.Len() != 3 || !f.MayContain([]byte("a")) {
		t.Errorf("Len() = %d, MayContain(a) = %v", f.Len(), f.MayContain([]byte("a")))
	}
}

func TestOptions(t *testing.T) {
	for _, opt := range []Option{
		WithFingerprintBits(0),
		WithFingerprintBits(33),
		WithLoadFactor(0.3),
		WithLoadFactor(1),
	} {
		if _, err := New(opt); err == nil {
			t.Error("New accepted an option that is out of range")
		}
		if _, err := Build(slices.Values([][]byte{}), opt); err == nil {
			t.Error("Build accepted an option that is out of range")
		}
	}
	f := mustNew(t, WithFingerprintBits(32), WithLoadFactor(0.5), WithSeed(7))
	if f.width != 33 || f.load != 0.5 {
		t.Errorf("options not applied: width %d, load %v", f.width, f.load)
	}
}

func TestEmpty(t *testing.T) {
	f := mustNew(t)
	if f.Len() != 0 || f.MayContain([]byte("x")) || f.MayContainRange(nil, []byte("\xff")) || f.Delete([]byte("x")) {
		t.Error("empty filter claims a key")
	}
	if err := f.check(); err != nil {
		t.Fatal(err)
	}
	if f.SizeBytes() <= 0 {
		t.Error("SizeBytes() of an empty filter is not positive")
	}
}

// TestExactWhileSmall checks that a filter with fewer keys than a store holds
// keeps them in full and so has no false positives at all.
func TestExactWhileSmall(t *testing.T) {
	r := rand.New(rand.NewPCG(8, 8))
	f := mustNew(t)
	o := newOracle()
	for range 500 {
		k := smallIntKey(r)
		f.Insert(k)
		o.add(k)
	}
	for i := range 5000 {
		k := binary.BigEndian.AppendUint64(nil, uint64(i))
		if got, want := f.MayContain(k), o.has(k); got != want {
			t.Fatalf("MayContain(%d) = %v, want %v", i, got, want)
		}
		hi := binary.BigEndian.AppendUint64(nil, uint64(i+3))
		if got, want := f.MayContainRange(k, hi), o.hasRange(k, hi); got != want {
			t.Fatalf("MayContainRange(%d, %d) = %v, want %v", i, i+3, got, want)
		}
	}
	for len(o.all) > 0 {
		if !f.Delete(o.take(r)) {
			t.Fatal("Delete found nothing")
		}
	}
	if f.Len() != 0 || len(f.samples) != 0 || f.MayContainRange(nil, []byte("\xff")) {
		t.Errorf("filter is not empty after deleting every key: %d samples", len(f.samples))
	}
}

func TestRepeatedKey(t *testing.T) {
	f := mustNew(t)
	k := []byte("again")
	for range 3 {
		f.Insert(k)
	}
	for i := range 3 {
		if !f.MayContain(k) || !f.Delete(k) {
			t.Fatalf("occurrence %d is missing", i)
		}
	}
	if f.MayContain(k) || f.Delete(k) || f.Len() != 0 {
		t.Error("key survives its last delete")
	}
}

// TestDeleteAbsent checks that deleting a key no entry stands for reports
// false and changes nothing, wherever the key would live.
func TestDeleteAbsent(t *testing.T) {
	r := rand.New(rand.NewPCG(12, 12))
	keys := make([][]byte, 3000)
	for i := range keys {
		keys[i] = uniformKey(r)
	}
	slices.SortFunc(keys, bytes.Compare)
	f, err := Build(slices.Values(keys), WithFingerprintBits(16))
	if err != nil {
		t.Fatal(err)
	}
	f.Insert(nil)                           // below the first sample
	f.Insert(bytes.Repeat([]byte{0xff}, 9)) // above the last one
	size := f.SizeBytes()
	if size < 3000*2 {
		t.Errorf("SizeBytes() = %d for 3002 keys", size)
	}

	misses := 0
	for range 2000 {
		k := uniformKey(r)
		if !f.Delete(k) {
			misses++
		}
	}
	// With 16 fingerprint bits next to none of the random keys has an entry.
	if misses < 1990 || f.Len() != 3002-(2000-misses) {
		t.Errorf("%d of 2000 absent keys had no entry; Len() = %d", misses, f.Len())
	}
	if f.Delete([]byte{0x00, 0x00, 0x01}) || f.Delete(bytes.Repeat([]byte{0xff}, 10)) {
		t.Error("Delete found a key that is not buffered")
	}
	if err := f.check(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyRange(t *testing.T) {
	f := mustNew(t)
	f.Insert([]byte("m"))
	if f.MayContainRange([]byte("z"), []byte("a")) {
		t.Error("a range with lo > hi is not empty")
	}
	if !f.MayContainRange([]byte("a"), []byte("z")) {
		t.Error("false negative")
	}
}

// TestBoundarySamples deletes and reinserts the smallest and largest keys,
// which are always samples, around flushes of both buffers.
func TestBoundarySamples(t *testing.T) {
	key := func(i int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(i)) }
	f := mustNew(t)
	f.period, f.burst = 1<<60, 4
	o := newOracle()
	insert := func(i int) { f.Insert(key(i)); o.add(key(i)); verify(t, f, o) }
	remove := func(i int) {
		t.Helper()
		o.remove(key(i))
		if !f.Delete(key(i)) {
			t.Fatalf("Delete(%d) found nothing", i)
		}
		verify(t, f, o)
	}

	for i := 100; i < 120; i++ { // grows upwards: 100 is the first sample
		insert(i)
	}
	remove(100) // first sample with a store behind it: stays as a boundary
	if f.samples[0].present {
		t.Error("deleted first sample is still present")
	}
	if f.MayContain(key(100)) {
		t.Error("deleted first sample is still reported")
	}
	insert(100) // and comes back
	if !f.samples[0].present {
		t.Error("reinserted first sample is not present")
	}
	remove(100)

	last := len(f.samples) - 1
	top := int(binary.BigEndian.Uint32(f.samples[last].key))
	remove(top + 1) // make room so the repeat below does not flush the buffer
	insert(top)     // a repeat of the last sample waits in the buffer
	if len(f.above) != 3 || !bytes.Equal(f.above[0], key(top)) {
		t.Fatalf("repeat of the last sample is not buffered: %x", f.above)
	}
	remove(top)
	remove(top) // now the sample itself goes
	if f.samples[last].present || f.MayContain(key(top)) {
		t.Error("deleted last sample is still reported")
	}

	for i := 99; i > 80; i-- { // grows downwards past the deleted first sample
		insert(i)
	}
	for i := 130; i < 150; i++ { // and upwards past the deleted last sample
		insert(i)
	}
	for i := range f.samples {
		if !f.samples[i].present {
			t.Errorf("sample %d is still a deleted boundary after both ends grew past it", i)
		}
	}

	// Deleting interior samples merges their stores.
	before := len(f.samples)
	for _, s := range slices.Clone(f.samples[1 : len(f.samples)-1]) {
		remove(int(binary.BigEndian.Uint32(append(bytes.Clone(s.key), 0, 0, 0)[:4])))
	}
	if len(f.samples) != 2 || before <= 2 {
		t.Errorf("%d samples left of %d, want 2", len(f.samples), before)
	}

	// Emptying the stores lets the end samples go too.
	for len(o.all) > 0 {
		i := int(binary.BigEndian.Uint32(append([]byte(o.all[0]), 0, 0, 0)[:4]))
		remove(i)
	}
	if len(f.samples) != 0 {
		t.Errorf("%d samples left in an empty filter", len(f.samples))
	}
}

// TestSoleSampleDeleted covers a filter whose only sample is deleted while
// keys remain on both sides of it.
func TestSoleSampleDeleted(t *testing.T) {
	f := mustNew(t)
	o := newOracle()
	for _, k := range []string{"m", "a", "z"} {
		f.Insert([]byte(k))
		o.add([]byte(k))
	}
	if !f.Delete([]byte("m")) {
		t.Fatal("Delete(m) found nothing")
	}
	o.remove([]byte("m"))
	verify(t, f, o)
	if f.MayContain([]byte("m")) || f.Delete([]byte("m")) {
		t.Error("deleted key is still there")
	}
	if !f.Delete([]byte("a")) || !f.Delete([]byte("z")) {
		t.Fatal("Delete found nothing")
	}
	if len(f.samples) != 0 || f.Len() != 0 {
		t.Errorf("filter not empty: %d samples, Len() = %d", len(f.samples), f.Len())
	}
}

// TestAppendMatchesBuild checks that inserting keys in ascending order ends
// in the same structure as building from them.
func TestAppendMatchesBuild(t *testing.T) {
	keys := make([][]byte, 5000)
	for i := range keys {
		keys[i] = binary.BigEndian.AppendUint64(nil, uint64(i)*977)
	}
	built, err := Build(slices.Values(keys))
	if err != nil {
		t.Fatal(err)
	}
	appended := mustNew(t)
	for _, k := range keys {
		appended.Insert(k)
	}
	if got, want := len(appended.samples), len(built.samples); got < want-1 || got > want {
		t.Errorf("appending made %d samples, building %d", got, want)
	}
	for i := range min(len(appended.samples), len(built.samples)) - 1 {
		a, b := appended.samples[i], built.samples[i]
		if !bytes.Equal(a.key, b.key) || !slices.Equal(a.store.words, b.store.words) {
			t.Fatalf("store %d differs between appending and building", i)
		}
	}
}

// TestSplitDeclined checks that a key chosen as a sample stays an ordinary
// entry when an existing entry already stands for it: the key behind that
// entry could lie on either side of the new sample.
func TestSplitDeclined(t *testing.T) {
	key := func(i int) []byte { return fmt.Appendf(nil, "a%040d", i) }
	f := mustNew(t, WithFingerprintBits(2))
	f.burst = 4
	// A store between "a" and "b" cannot tell these keys apart: they agree
	// on far more bits than its entries hold.
	f.Insert([]byte("a"))
	for i := range 3 {
		f.Insert(key(i))
	}
	f.Insert([]byte("b"))
	if len(f.samples) != 2 || f.samples[0].store.count != 3 {
		t.Fatalf("setup made %d samples", len(f.samples))
	}

	f.period = 1 // every insert now tries to become a sample
	for i := 3; i < 40; i++ {
		f.Insert(key(i))
	}
	if len(f.samples) != 2 {
		t.Errorf("indistinguishable keys split their store into %d", len(f.samples)-1)
	}
	if err := f.check(); err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		if !f.MayContain(key(i)) {
			t.Fatalf("false negative for key %d", i)
		}
	}
}

// TestSplitAndMerge checks that sampled keys divide their store and that
// deleting them joins the pieces again.
func TestSplitAndMerge(t *testing.T) {
	key := func(i int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(i)) }
	var keys [][]byte
	for i := range 2000 {
		keys = append(keys, key(i*1000+1))
	}
	f, err := Build(slices.Values(keys))
	if err != nil {
		t.Fatal(err)
	}
	o := newOracle()
	for _, k := range keys {
		o.add(k)
	}
	built := len(f.samples)

	f.period = 1
	for i := range 200 {
		k := key(i*9973 + 500)
		f.Insert(k)
		o.add(k)
	}
	verify(t, f, o)
	if len(f.samples) < built+150 {
		t.Errorf("200 sampled inserts added %d samples", len(f.samples)-built)
	}

	for i := range 200 {
		k := key(i*9973 + 500)
		if !f.Delete(k) {
			t.Fatalf("Delete(%x) found nothing", k)
		}
		o.remove(k)
	}
	verify(t, f, o)
	if len(f.samples) != built {
		t.Errorf("%d samples after deleting the sampled keys, want %d", len(f.samples), built)
	}
}

// TestFalsePositiveRate checks that a built filter's false positive rate is
// in line with its fingerprint size.
func TestFalsePositiveRate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	r := rand.New(rand.NewPCG(9, 9))
	const n = 200_000
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = uniformKey(r)
	}
	slices.SortFunc(keys, bytes.Compare)

	for _, c := range []struct {
		bits  int
		limit float64 // on the point false positive rate
	}{{4, 0.2}, {8, 0.012}, {12, 0.0008}} {
		f, err := Build(slices.Values(keys), WithFingerprintBits(c.bits))
		if err != nil {
			t.Fatal(err)
		}
		const probes = 200_000
		point, short := 0, 0
		for range probes {
			x := r.Uint64()
			k := binary.BigEndian.AppendUint64(nil, x)
			if f.MayContain(k) {
				point++
			}
			// A range a thousandth of the average gap between keys wide
			// is almost surely empty.
			if f.MayContainRange(k, binary.BigEndian.AppendUint64(nil, x+(1<<64/n)/1000)) {
				short++
			}
		}
		pointRate, shortRate := float64(point)/probes, float64(short)/probes
		t.Logf("%2d bits: %.2f bits per key, point FPR %.5f, short range FPR %.5f",
			c.bits, float64(8*f.SizeBytes())/n, pointRate, shortRate)
		if pointRate > c.limit {
			t.Errorf("%d bits: point FPR %.5f exceeds %.5f", c.bits, pointRate, c.limit)
		}
		if shortRate > 2.5*c.limit {
			t.Errorf("%d bits: short range FPR %.5f exceeds %.5f", c.bits, shortRate, 2.5*c.limit)
		}
	}

	// The same keys inserted one by one in random order. An entry loses a
	// bit of resolution each time its store splits, so the rate grows with
	// the logarithm of the growth; the paper bounds it by e/2*(log2(n)+2),
	// where e is the rate of a built filter. That is 7.7% here.
	f := mustNew(t)
	for _, i := range r.Perm(n) {
		f.Insert(keys[i])
	}
	const probes = 200_000
	point := 0
	for range probes {
		if f.MayContain(uniformKey(r)) {
			point++
		}
	}
	rate := float64(point) / probes
	t.Logf("inserted one by one: %.2f bits per key, %d samples, point FPR %.5f",
		float64(8*f.SizeBytes())/n, len(f.samples), rate)
	if rate > 0.077 {
		t.Errorf("point FPR %.5f after random inserts exceeds 0.077", rate)
	}
}
