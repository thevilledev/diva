package diva

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
)

// tinyKey decodes a key of up to three bytes drawn from four byte values.
// The small universe makes keys collide, repeat and differ only in trailing
// zeros, and the values chosen produce long runs of equal bits.
func tinyKey(b byte) []byte {
	alphabet := [4]byte{0x00, 0x01, 0x80, 0xff}
	k := make([]byte, b>>6)
	for i := range k {
		k[i] = alphabet[b>>(2*i)&3]
	}
	return k
}

// replay interprets data as a filter configuration followed by a sequence of
// inserts and deletes over tiny keys. It checks the filter's invariants and
// the absence of false negatives after every step, and every possible range
// at the end.
//
//nolint:thelper // replay is the test body: failures should report its lines.
func replay(t *testing.T, data []byte) {
	if len(data) == 0 {
		return
	}
	c := data[0]
	f := mustNew(t, WithFingerprintBits(1+int(c%5)), WithSeed(uint64(c)))
	f.period = 1 + uint64(c>>3)%4
	f.burst = 2 + int(c>>5)%4
	o := newOracle()

	for data = data[1:]; len(data) >= 2; data = data[2:] {
		k := tinyKey(data[1])
		switch {
		case data[0]%8 < 5:
			f.Insert(k)
			o.add(k)
		case o.has(k):
			if !f.Delete(k) {
				t.Fatalf("Delete(%x) found nothing", k)
			}
			o.remove(k)
		}
		if err := f.check(); err != nil {
			t.Fatal(err)
		}
		if f.Len() != len(o.all) {
			t.Fatalf("Len() = %d, want %d", f.Len(), len(o.all))
		}
		for _, k := range o.keys() {
			if !f.MayContain([]byte(k)) {
				t.Fatalf("false negative for key %x", k)
			}
		}
	}

	var universe [][]byte
	for b := range 256 {
		universe = append(universe, tinyKey(byte(b)))
	}
	slices.SortFunc(universe, bytes.Compare)
	universe = slices.CompactFunc(universe, bytes.Equal)
	for i, lo := range universe {
		for _, hi := range universe[i:] {
			if o.hasRange(lo, hi) && !f.MayContainRange(lo, hi) {
				t.Fatalf("false negative for range [%x, %x]", lo, hi)
			}
		}
	}
}

func FuzzFilter(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{0x00, 0, 0x40, 0, 0x41, 0, 0x42, 0, 0x43, 5, 0x41, 0, 0x81, 0, 0xc6})
	f.Add([]byte{0xff, 0, 0xc0, 0, 0xff, 0, 0x7f, 0, 0x80, 0, 0x55, 7, 0xc0, 7, 0xff, 0, 0x01})
	f.Add(bytes.Repeat([]byte{0x2b, 0, 0x9a}, 40))
	f.Fuzz(replay)
}

// TestTinyKeys drives replay with random data, so the fuzz target's checks
// also run as part of the ordinary tests.
func TestTinyKeys(t *testing.T) {
	rounds := 600
	if testing.Short() {
		rounds = 100
	}
	r := rand.New(rand.NewPCG(10, 10))
	for range rounds {
		data := make([]byte, 1+2*r.IntN(400))
		for i := range data {
			data[i] = byte(r.Uint32())
		}
		replay(t, data)
	}
}
