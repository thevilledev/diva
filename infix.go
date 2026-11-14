package diva

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

// Keys are read as bit strings, most significant bit first, that continue
// with zeros forever. Two keys that differ only in trailing zero bytes are
// therefore the same key to the filter, and canon picks the representative:
// the key without its trailing zero bytes. On canonical keys bytes.Compare
// agrees with the order of the padded bit strings.
func canon(key []byte) []byte { return bytes.TrimRight(key, "\x00") }

// wordAt returns the 64 bits of k that start at byte i.
func wordAt(k []byte, i int) uint64 {
	if i+8 <= len(k) {
		return binary.BigEndian.Uint64(k[i:])
	}
	if i >= len(k) {
		return 0
	}
	var buf [8]byte
	copy(buf[:], k[i:])
	return binary.BigEndian.Uint64(buf[:])
}

// bitAt returns bit pos of k.
func bitAt(k []byte, pos int) uint64 {
	if i := pos >> 3; i < len(k) {
		return uint64(k[i]>>(7-uint(pos)&7)) & 1
	}
	return 0
}

// bitsAt returns the n bits of k that start at bit pos, for n in [0, 64].
func bitsAt(k []byte, pos, n int) uint64 {
	i, off := pos>>3, uint(pos&7)
	w := wordAt(k, i) << off
	if off != 0 {
		w |= wordAt(k, i+8) >> (64 - off)
	}
	return w >> uint(64-n)
}

// sharedBits returns the length of the longest common bit prefix of a and b,
// which must be distinct canonical keys.
func sharedBits(a, b []byte) int {
	for i := 0; i < max(len(a), len(b)); i += 8 {
		if x := wordAt(a, i) ^ wordAt(b, i); x != 0 {
			return i<<3 + bits.LeadingZeros64(x)
		}
	}
	panic("diva: adjacent samples are equal")
}

// span describes how keys between two adjacent samples are turned into
// infixes. With prev < next the samples, a key between them looks like
//
//	[shared bits][d][redundant bits][payload ...]
//
// The shared bits are the common prefix of the samples. d is the first bit
// where they differ: 0 in prev, 1 in next. The redundant bits are the run
// that follows in which prev has ones and next has zeros; a key between the
// samples must repeat the complement of its d there, so the run carries no
// information. The infix of a key is d followed by the payload bits, and its
// top qbits bits form the quotient.
//
// Skipping the redundant bits guarantees that the samples' own quotients are
// far enough apart for a store to see between storeTarget/2 and storeTarget
// distinct quotients, which is what keeps runs short.
type span struct {
	shared    int32
	redundant int32
	qbits     uint8  // quotient width: quotientBits, or one more
	base      uint16 // quotient of prev; store quotients are relative to it
	quotients uint16 // number of quotients between prev and next, inclusive
}

func newSpan(prev, next []byte) span {
	shared := sharedBits(prev, next)
	pos := shared + 1
	for {
		// Leading zeros of x count positions where prev has 1 and next has 0.
		x := ^bitsAt(prev, pos, 64) | bitsAt(next, pos, 64)
		n := bits.LeadingZeros64(x)
		pos += n
		if n < 64 {
			break
		}
	}

	// Use one more quotient bit when the samples are close enough that
	// quotientBits of them would leave fewer than storeTarget/2 quotients.
	qbits := quotientBits
	lo := bitsAt(prev, pos, qbits-1)
	hi := 1<<(qbits-1) | bitsAt(next, pos, qbits-1)
	if 2*(hi-lo+1) < storeTarget {
		qbits++
		lo = bitsAt(prev, pos, qbits-1)
		hi = 1<<(qbits-1) | bitsAt(next, pos, qbits-1)
	}
	return span{
		shared:    int32(shared),
		redundant: int32(pos - shared - 1),
		qbits:     uint8(qbits),
		base:      uint16(lo),
		quotients: uint16(hi - lo + 1),
	}
}

// start returns the position of the first payload bit.
func (sp span) start() int { return int(sp.shared) + int(sp.redundant) + 1 }

// entry returns the full-length entry of key k, which must not be smaller
// than the span's left sample nor larger than its right one. w is the slot
// width. The mapping is monotone in k.
func (sp span) entry(k []byte, w int) uint64 {
	width := int(sp.qbits) - 1 + w
	infix := bitAt(k, int(sp.shared))<<uint(width) | bitsAt(k, sp.start(), width)
	return (infix | 1) - uint64(sp.base)<<uint(w)
}

// lastEntry returns the largest entry a store with this span can hold.
func (sp span) lastEntry(w int) uint64 { return uint64(sp.quotients)<<uint(w) - 1 }

// origin identifies the store an entry is being moved out of.
type origin struct {
	span span
	prev []byte // left sample of that store
}

// prefix reconstructs the leading bits of the key behind an entry: the bits
// its store's samples share, the differentiating bit, the redundant bits
// that bit implies, and the payload the entry carries.
type prefix struct {
	prev    []byte
	shared  int    // bits taken from prev
	start   int    // position of the first payload bit
	diff    uint64 // the differentiating bit
	payload uint64
	width   int // payload width, delimiter included
}

// bits returns the n bits starting at pos, for n in [0, 64]. The range must
// lie within the bits the entry knows.
func (p prefix) bits(pos, n int) uint64 {
	var out uint64
	for n > 0 {
		var chunk uint64
		m := n
		switch {
		case pos < p.shared:
			m = min(n, p.shared-pos)
			chunk = bitsAt(p.prev, pos, m)
		case pos == p.shared:
			m, chunk = 1, p.diff
		case pos < p.start:
			m = min(n, p.start-pos)
			if p.diff == 0 {
				chunk = mask(m)
			}
		default:
			chunk = p.payload >> uint(p.width-(pos-p.start)-m) & mask(m)
		}
		out = out<<uint(m) | chunk
		pos += m
		n -= m
	}
	return out
}

// recode appends to dst the entries that stand for entry v of the store
// described by from once the key behind it lives in a store with span to.
// It is how entries survive a split or a merge of their store.
//
// The new store reads a different window of the key. Where that window
// reaches past the bits v knows, the result is a shorter entry. If v does
// not even know its new quotient, it becomes one minimal entry for every
// quotient it could have, so the key stays covered wherever it really is.
func recode(dst []uint64, v uint64, from origin, to span, w int) []uint64 {
	old := from.span
	oldWidth := int(old.qbits) - 1 + w
	abs := v + uint64(old.base)<<uint(w)
	p := prefix{
		prev:    from.prev,
		shared:  int(old.shared),
		start:   old.start(),
		diff:    abs >> uint(oldWidth),
		payload: abs & mask(oldWidth),
		width:   oldWidth,
	}
	// Number of leading key bits the entry pins down.
	known := p.start + oldWidth - 1 - bits.TrailingZeros64(v)

	qbits, width := int(to.qbits), int(to.qbits)-1+w
	have := known - to.start() // payload bits available under the new span
	base := uint64(to.base)
	top := base + uint64(to.quotients) - 1

	if have >= qbits-1 {
		have = min(have, width-1)
		infix := p.bits(int(to.shared), 1)<<uint(width) |
			p.bits(to.start(), have)<<uint(width-have) |
			1<<uint(width-have-1)
		q := min(max(infix>>uint(w), base), top)
		return append(dst, (q-base)<<uint(w)|infix&mask(w))
	}

	lo, hi := base, top
	if known > int(to.shared) {
		have = max(have, 0)
		free := qbits - 1 - have
		first := p.bits(int(to.shared), 1)<<uint(qbits-1) | p.bits(to.start(), have)<<uint(free)
		lo, hi = max(lo, first), min(hi, first|mask(free))
	}
	for q := lo; q <= hi; q++ {
		dst = append(dst, (q-base)<<uint(w)|1<<uint(w-1))
	}
	return dst
}
