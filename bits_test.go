package diva

import (
	"math/rand/v2"
	"slices"
	"testing"
)

// bitModel is the obvious implementation the packed bitmaps are checked
// against: one bool per bit.
type bitModel []bool

func (m bitModel) pack() []uint64 {
	ws := make([]uint64, (len(m)+63)/64)
	for i, b := range m {
		if b {
			ws[i/64] |= 1 << (i % 64)
		}
	}
	return ws
}

func randomBits(r *rand.Rand, n int, density float64) bitModel {
	m := make(bitModel, n)
	for i := range m {
		m[i] = r.Float64() < density
	}
	return m
}

func TestMask(t *testing.T) {
	for n, want := range map[int]uint64{0: 0, 1: 1, 9: 0x1ff, 63: 1<<63 - 1, 64: ^uint64(0)} {
		if got := mask(n); got != want {
			t.Errorf("mask(%d) = %#x, want %#x", n, got, want)
		}
	}
}

func TestGetSetBits(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 1))
	for range 2000 {
		m := randomBits(r, 320, 0.5)
		ws := m.pack()
		n := r.IntN(65)
		pos := r.IntN(len(m) - n + 1)

		var want uint64
		for i := range n {
			if m[pos+i] {
				want |= 1 << i
			}
		}
		if got := getBits(ws, pos, n); got != want {
			t.Fatalf("getBits(pos=%d, n=%d) = %#x, want %#x", pos, n, got, want)
		}

		v := r.Uint64()
		setBits(ws, pos, n, v)
		for i := range n {
			m[pos+i] = v>>i&1 != 0
		}
		if want := m.pack(); !slices.Equal(ws, want) {
			t.Fatalf("setBits(pos=%d, n=%d) disturbed other bits", pos, n)
		}
	}
}

func TestSingleBits(t *testing.T) {
	ws := make([]uint64, 3)
	for _, i := range []int{0, 63, 64, 130, 191} {
		setBit(ws, i)
		if !testBit(ws, i) {
			t.Errorf("bit %d not set", i)
		}
	}
	clearBit(ws, 64)
	if testBit(ws, 64) || !testBit(ws, 63) || !testBit(ws, 130) {
		t.Errorf("clearBit(64) left %#x", ws)
	}
}

func TestShift(t *testing.T) {
	r := rand.New(rand.NewPCG(2, 2))
	for range 3000 {
		m := randomBits(r, 400, 0.5)
		ws := m.pack()
		by := 1 + r.IntN(40)
		lo := by + r.IntN(len(m)-2*by)
		hi := lo + r.IntN(len(m)-by-lo+1)

		want := slices.Clone(m)
		up := r.IntN(2) == 0
		if up {
			shiftUp(ws, lo, hi, by)
			for i := lo; i < hi; i++ {
				want[i+by] = m[i]
			}
			for i := lo; i < min(lo+by, hi); i++ {
				want[i] = false
			}
		} else {
			shiftDown(ws, lo, hi, by)
			for i := lo; i < hi; i++ {
				want[i-by] = m[i]
			}
			for i := max(hi-by, lo); i < hi; i++ {
				want[i] = false
			}
		}
		if !slices.Equal(ws, want.pack()) {
			t.Fatalf("shift(up=%v, lo=%d, hi=%d, by=%d) is wrong", up, lo, hi, by)
		}
	}
}

func TestScan(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	for _, density := range []float64{0, 0.01, 0.3, 1} {
		for range 200 {
			m := randomBits(r, 256, density)
			ws := m.pack()
			var ones []int
			for i, b := range m {
				if b {
					ones = append(ones, i)
				}
			}

			from := r.IntN(len(m) + 40)
			next := len(m)
			if i, _ := slices.BinarySearch(ones, from); i < len(ones) {
				next = ones[i]
			}
			if got := nextSet(ws, from); got != next {
				t.Fatalf("nextSet(%d) = %d, want %d", from, got, next)
			}

			from = r.IntN(len(m)+1) - 1
			prev := -1
			if i, _ := slices.BinarySearch(ones, from+1); i > 0 {
				prev = ones[i-1]
			}
			if got := prevSet(ws, from); got != prev {
				t.Fatalf("prevSet(%d) = %d, want %d", from, got, prev)
			}

			at := r.IntN(len(m) + 1)
			below, _ := slices.BinarySearch(ones, at)
			if got := rank(ws, at); got != below {
				t.Fatalf("rank(%d) = %d, want %d", at, got, below)
			}

			k := r.IntN(len(ones) + 2)
			want := len(m)
			if k < len(ones) {
				want = ones[k]
			}
			if got := selectBit(ws, k); got != want {
				t.Fatalf("selectBit(%d) = %d, want %d", k, got, want)
			}
		}
	}
}
