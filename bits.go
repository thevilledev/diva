package diva

import "math/bits"

// This file holds the bit-level primitives the infix store is built on. A
// bitmap is a []uint64 whose bit i lives in word i/64 at position i%64.

// mask returns a word with the low n bits set, for n in [0, 64].
func mask(n int) uint64 { return 1<<uint(n) - 1 }

// getBits returns the n bits starting at bit pos, for n in [0, 64].
func getBits(ws []uint64, pos, n int) uint64 {
	i, off := pos>>6, uint(pos&63)
	v := ws[i] >> off
	if off+uint(n) > 64 {
		v |= ws[i+1] << (64 - off)
	}
	return v & mask(n)
}

// setBits overwrites the n bits starting at bit pos with the low n bits of v.
func setBits(ws []uint64, pos, n int, v uint64) {
	i, off := pos>>6, uint(pos&63)
	m := mask(n)
	v &= m
	ws[i] = ws[i]&^(m<<off) | v<<off
	if off+uint(n) > 64 {
		spill := 64 - off
		ws[i+1] = ws[i+1]&^(m>>spill) | v>>spill
	}
}

func testBit(ws []uint64, i int) bool { return ws[i>>6]>>(uint(i)&63)&1 != 0 }
func setBit(ws []uint64, i int)       { ws[i>>6] |= 1 << (uint(i) & 63) }
func clearBit(ws []uint64, i int)     { ws[i>>6] &^= 1 << (uint(i) & 63) }

// clearBits zeroes the bits in [lo, hi).
func clearBits(ws []uint64, lo, hi int) {
	for lo < hi {
		n := min(64, hi-lo)
		setBits(ws, lo, n, 0)
		lo += n
	}
}

// shiftUp moves the bits in [lo, hi) to [lo+by, hi+by) and zeroes the bits
// they vacate. The caller owns the destination range.
func shiftUp(ws []uint64, lo, hi, by int) {
	// Copy from the top so a chunk never lands on bits that still have to move.
	for pos := hi; pos > lo; {
		n := min(64, pos-lo)
		pos -= n
		setBits(ws, pos+by, n, getBits(ws, pos, n))
	}
	clearBits(ws, lo, min(lo+by, hi))
}

// shiftDown moves the bits in [lo, hi) to [lo-by, hi-by) and zeroes the bits
// they vacate. The caller owns the destination range.
func shiftDown(ws []uint64, lo, hi, by int) {
	for pos := lo; pos < hi; {
		n := min(64, hi-pos)
		setBits(ws, pos-by, n, getBits(ws, pos, n))
		pos += n
	}
	clearBits(ws, max(hi-by, lo), hi)
}

// nextSet returns the index of the first set bit at or after from, or
// 64*len(ws) if there is none.
func nextSet(ws []uint64, from int) int {
	i := from >> 6
	if i >= len(ws) {
		return len(ws) << 6
	}
	w := ws[i] &^ mask(from&63)
	for w == 0 {
		i++
		if i == len(ws) {
			return len(ws) << 6
		}
		w = ws[i]
	}
	return i<<6 + bits.TrailingZeros64(w)
}

// prevSet returns the index of the last set bit at or before from, or -1 if
// there is none. A negative from yields -1.
func prevSet(ws []uint64, from int) int {
	if from < 0 {
		return -1
	}
	i := from >> 6
	w := ws[i] & mask(from&63+1)
	for w == 0 {
		i--
		if i < 0 {
			return -1
		}
		w = ws[i]
	}
	return i<<6 + 63 - bits.LeadingZeros64(w)
}

// rank returns the number of set bits in [0, i).
func rank(ws []uint64, i int) int {
	n := 0
	for _, w := range ws[:i>>6] {
		n += bits.OnesCount64(w)
	}
	if r := i & 63; r != 0 {
		n += bits.OnesCount64(ws[i>>6] & mask(r))
	}
	return n
}

// selectBit returns the index of the k-th set bit, counting from zero, or
// 64*len(ws) if fewer than k+1 bits are set.
func selectBit(ws []uint64, k int) int {
	for i, w := range ws {
		c := bits.OnesCount64(w)
		if k < c {
			for ; k > 0; k-- {
				w &= w - 1
			}
			return i<<6 + bits.TrailingZeros64(w)
		}
		k -= c
	}
	return len(ws) << 6
}
