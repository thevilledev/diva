package diva

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// keyFromBits builds a key from a string of '0' and '1'; spaces are ignored.
func keyFromBits(s string) []byte {
	s = strings.ReplaceAll(s, " ", "")
	k := make([]byte, (len(s)+7)/8)
	for i, c := range s {
		if c == '1' {
			k[i/8] |= 0x80 >> (i % 8)
		}
	}
	return k
}

// naiveBits is bitsAt, one bit at a time.
func naiveBits(k []byte, pos, n int) (v uint64) {
	for i := pos; i < pos+n; i++ {
		v <<= 1
		if i/8 < len(k) && k[i/8]&(0x80>>(i%8)) != 0 {
			v |= 1
		}
	}
	return v
}

// naiveSpan derives the shared and redundant bit counts one bit at a time.
func naiveSpan(prev, next []byte) (shared, redundant int) {
	for naiveBits(prev, shared, 1) == naiveBits(next, shared, 1) {
		shared++
	}
	for naiveBits(prev, shared+1+redundant, 1) == 1 && naiveBits(next, shared+1+redundant, 1) == 0 {
		redundant++
	}
	return shared, redundant
}

// edgyKey returns a short key built mostly from bytes that produce long runs
// of equal bits, behind one of a few shared prefixes.
func edgyKey(r *rand.Rand) []byte {
	alphabet := []byte{0x00, 0xff, 0x7f, 0x80, 0x01, 0xfe, byte(r.Uint32())}
	k := bytes.Repeat([]byte{0xa5}, r.IntN(4))
	for range r.IntN(12) {
		k = append(k, alphabet[r.IntN(len(alphabet))])
	}
	return k
}

// sortedKeys returns n distinct canonical keys in ascending order.
func sortedKeys(r *rand.Rand, n int, gen func(*rand.Rand) []byte) [][]byte {
	keys := make([][]byte, 0, n)
	for len(keys) < n {
		keys = append(keys, canon(gen(r)))
		slices.SortFunc(keys, bytes.Compare)
		keys = slices.CompactFunc(keys, bytes.Equal)
	}
	return keys
}

func TestCanon(t *testing.T) {
	for in, want := range map[string]string{
		"":            "",
		"\x00":        "",
		"\x00\x00":    "",
		"a":           "a",
		"a\x00":       "a",
		"a\x00\x00":   "a",
		"a\x00b":      "a\x00b",
		"\x00a\x00":   "\x00a",
		"\xff\xff":    "\xff\xff",
		"ab\x00c\x00": "ab\x00c",
	} {
		if got := canon([]byte(in)); string(got) != want {
			t.Errorf("canon(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBitsAt(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 4))
	for range 5000 {
		k := make([]byte, r.IntN(20))
		for i := range k {
			k[i] = byte(r.Uint32())
		}
		pos, n := r.IntN(200), r.IntN(65)
		if got, want := bitsAt(k, pos, n), naiveBits(k, pos, n); got != want {
			t.Fatalf("bitsAt(%x, %d, %d) = %#x, want %#x", k, pos, n, got, want)
		}
		if got, want := bitAt(k, pos), naiveBits(k, pos, 1); got != want {
			t.Fatalf("bitAt(%x, %d) = %d, want %d", k, pos, got, want)
		}
	}
}

// TestSpanPaperExamples replays Figure 3 of the paper.
func TestSpanPaperExamples(t *testing.T) {
	// Part A: no redundant bits.
	sp := newSpan(keyFromBits("0101 0011011 101"), keyFromBits("0101 1110111 0011"))
	if sp.shared != 4 || sp.redundant != 0 {
		t.Errorf("part A: shared %d redundant %d, want 4 and 0", sp.shared, sp.redundant)
	}

	// Part B: five redundant bits follow the differentiating bit.
	prev := keyFromBits("0101 0 11111 011011 101")
	next := keyFromBits("0101 1 00000 110111 0011")
	sp = newSpan(prev, next)
	if sp.shared != 4 || sp.redundant != 5 {
		t.Fatalf("part B: shared %d redundant %d, want 4 and 5", sp.shared, sp.redundant)
	}
	// The infix of k is its differentiating bit and the bits after the
	// redundant run: 0 101001 ..., so its first seven bits are 0101001 = 41.
	const w = 9
	k := keyFromBits("0101 0 11111 101001 11")
	infix := sp.entry(k, w) + uint64(sp.base)<<w
	if got := infix >> (int(sp.qbits) + w - 7); got != 41 {
		t.Errorf("part B: infix starts with %07b, want 0101001", got)
	}
}

func TestSpanPanicsOnEqualSamples(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("newSpan accepted equal samples")
		}
	}()
	newSpan([]byte("same"), []byte("same"))
}

// TestSpanProperties checks, for random pairs of samples, that the span
// matches the bit-by-bit derivation and that entries are monotone and stay
// within the quotients the span announces.
func TestSpanProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	for round := range 300 {
		gen := edgyKey
		if round%3 == 0 {
			gen = func(r *rand.Rand) []byte { return []byte{0x61, byte(r.Uint32()), byte(r.Uint32()), byte(r.Uint32())} }
		}
		keys := sortedKeys(r, 40, gen)
		i := r.IntN(len(keys) - 1)
		j := i + 1 + r.IntN(len(keys)-i-1)
		prev, next := keys[i], keys[j]
		w := 2 + r.IntN(32)

		sp := newSpan(prev, next)
		shared, redundant := naiveSpan(prev, next)
		if int(sp.shared) != shared || int(sp.redundant) != redundant {
			t.Fatalf("newSpan(%x, %x): shared %d redundant %d, want %d and %d",
				prev, next, sp.shared, sp.redundant, shared, redundant)
		}
		if sp.quotients < storeTarget/2 || sp.quotients > storeTarget {
			t.Fatalf("newSpan(%x, %x) spans %d quotients", prev, next, sp.quotients)
		}
		if q := sp.entry(prev, w) >> w; q != 0 {
			t.Fatalf("left sample has quotient %d", q)
		}
		if q := sp.entry(next, w) >> w; q != uint64(sp.quotients)-1 {
			t.Fatalf("right sample has quotient %d of %d", q, sp.quotients)
		}
		if got, want := sp.lastEntry(w), uint64(sp.quotients-1)<<w|mask(w); got != want {
			t.Fatalf("lastEntry = %#x, want %#x", got, want)
		}

		var last uint64
		for _, k := range keys[i : j+1] {
			e := sp.entry(k, w)
			width := int(sp.qbits) - 1 + w
			want := naiveBits(k, shared, 1)<<width | naiveBits(k, shared+redundant+1, width) | 1
			if e+uint64(sp.base)<<w != want {
				t.Fatalf("entry(%x) = %#x, want infix %#x", k, e, want)
			}
			if e < last {
				t.Fatalf("entries are not monotone at %x", k)
			}
			if e&1 == 0 || e > sp.lastEntry(w) {
				t.Fatalf("entry(%x) = %#x is not a full-length entry of the span", k, e)
			}
			last = e
		}
	}
}

// TestRecodeCovers follows single keys through random sequences of splits
// and merges of the store they live in. Whatever recode turns a key's entry
// into must still stand for the key under the new span, and must stay within
// the new span's quotients.
func TestRecodeCovers(t *testing.T) {
	r := rand.New(rand.NewPCG(6, 6))
	expansions := 0
	for round := range 400 {
		gen := edgyKey
		if round%2 == 0 {
			gen = func(r *rand.Rand) []byte {
				return []byte{byte(r.IntN(3)), byte(r.Uint32()), byte(r.Uint32()), byte(r.Uint32()), byte(r.Uint32())}
			}
		}
		keys := sortedKeys(r, 60, gen)
		w := 2 + r.IntN(12)
		x := 1 + r.IntN(len(keys)-2) // the key we follow

		// Start with the widest store and its full-length entry.
		lo, hi := 0, len(keys)-1
		sp := newSpan(keys[lo], keys[hi])
		entries := []uint64{sp.entry(keys[x], w)}

		for range 12 {
			// Move one boundary: inwards is a split, outwards a merge.
			nlo, nhi := lo, hi
			if r.IntN(2) == 0 {
				nlo = r.IntN(x + 1)
			} else {
				nhi = x + 1 + r.IntN(len(keys)-x-1)
			}
			to := newSpan(keys[nlo], keys[nhi])
			from := origin{span: sp, prev: keys[lo]}

			var out []uint64
			for _, e := range entries {
				out = recode(out, e, from, to, w)
			}
			if len(out) > len(entries) {
				expansions++
			}
			want := to.entry(keys[x], w)
			covered := false
			for _, e := range out {
				if e == 0 || e > to.lastEntry(w) || e&mask(w) == 0 {
					t.Fatalf("recode produced %#x, outside a span of %d quotients", e, to.quotients)
				}
				covered = covered || e>>w == want>>w && covers(e, want)
			}
			if !covered {
				t.Fatalf("round %d: key %x lost moving from [%x, %x] to [%x, %x]",
					round, keys[x], keys[lo], keys[hi], keys[nlo], keys[nhi])
			}
			lo, hi, sp, entries = nlo, nhi, to, out
			if len(entries) > 4096 {
				break // the filter declines splits long before this
			}
		}
	}
	if expansions == 0 {
		t.Error("no recode had to expand an entry; the test lost its coverage")
	}
}

// TestRecodeKeepsFullLength checks the common case: recoding into the same
// span is the identity, and a merge keeps full-length entries full-length.
func TestRecodeKeepsFullLength(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 7))
	for range 200 {
		keys := sortedKeys(r, 30, edgyKey)
		const w = 9
		a, b, c := 0, 1+r.IntN(len(keys)-2), len(keys)-1
		inner, outer := newSpan(keys[a], keys[b]), newSpan(keys[a], keys[c])
		for _, k := range keys[a:b] {
			e := inner.entry(k, w)
			same := recode(nil, e, origin{span: inner, prev: keys[a]}, inner, w)
			if len(same) != 1 || same[0] != e {
				t.Fatalf("recode into the same span turned %#x into %#x", e, same)
			}
			merged := recode(nil, e, origin{span: inner, prev: keys[a]}, outer, w)
			if want := outer.entry(k, w); len(merged) != 1 || merged[0] != want {
				t.Fatalf("merge turned the entry of %x into %#x, want %#x", k, merged, want)
			}
		}
	}
}
