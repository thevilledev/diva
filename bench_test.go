package diva

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"slices"
	"testing"
)

const benchKeys = 1 << 20

func sortedUniformKeys(n int) [][]byte {
	r := rand.New(rand.NewPCG(42, 42))
	keys := make([][]byte, n)
	for i := range keys {
		keys[i] = binary.BigEndian.AppendUint64(nil, r.Uint64())
	}
	slices.SortFunc(keys, bytes.Compare)
	return keys
}

func builtFilter(b *testing.B) (*Filter, [][]byte) {
	b.Helper()
	keys := sortedUniformKeys(benchKeys)
	f, err := Build(slices.Values(keys))
	if err != nil {
		b.Fatal(err)
	}
	return f, keys
}

func BenchmarkBuild(b *testing.B) {
	keys := sortedUniformKeys(benchKeys)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Build(slices.Values(keys)); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/benchKeys, "ns/key")
}

func BenchmarkInsert(b *testing.B) {
	f, _ := builtFilter(b)
	r := rand.New(rand.NewPCG(1, 1))
	var k [8]byte
	b.ReportAllocs()
	for b.Loop() {
		binary.BigEndian.PutUint64(k[:], r.Uint64())
		f.Insert(k[:])
	}
}

func BenchmarkInsertDelete(b *testing.B) {
	f, _ := builtFilter(b)
	r := rand.New(rand.NewPCG(1, 1))
	var k [8]byte
	b.ReportAllocs()
	for b.Loop() {
		binary.BigEndian.PutUint64(k[:], r.Uint64())
		f.Insert(k[:])
		f.Delete(k[:])
	}
}

func BenchmarkMayContain(b *testing.B) {
	f, keys := builtFilter(b)
	b.Run("hit", func(b *testing.B) {
		// Cycle through a sample small enough to stay in cache, so the
		// benchmark measures the filter and not the fetching of its input.
		r := rand.New(rand.NewPCG(2, 2))
		probes := make([][8]byte, 4096)
		for i := range probes {
			copy(probes[i][:], keys[r.IntN(len(keys))])
		}
		i := 0
		for b.Loop() {
			if !f.MayContain(probes[i%len(probes)][:]) {
				b.Fatal("false negative")
			}
			i++
		}
	})
	b.Run("miss", func(b *testing.B) {
		r := rand.New(rand.NewPCG(2, 2))
		var k [8]byte
		for b.Loop() {
			binary.BigEndian.PutUint64(k[:], r.Uint64())
			f.MayContain(k[:])
		}
	})
}

func BenchmarkMayContainRange(b *testing.B) {
	f, keys := builtFilter(b)
	r := rand.New(rand.NewPCG(3, 3))
	var lo, hi [8]byte
	// The average gap between keys is 2^64/benchKeys = 2^44.
	for _, c := range []struct {
		name  string
		width uint64
	}{{"short", 1 << 30}, {"gap-sized", 1 << 44}, {"long", 1 << 54}} {
		b.Run(c.name, func(b *testing.B) {
			for b.Loop() {
				x := r.Uint64() >> 1
				binary.BigEndian.PutUint64(lo[:], x)
				binary.BigEndian.PutUint64(hi[:], x+c.width)
				f.MayContainRange(lo[:], hi[:])
			}
		})
	}
	b.Run("around-key", func(b *testing.B) {
		probes := make([]uint64, 4096)
		for i := range probes {
			probes[i] = binary.BigEndian.Uint64(keys[r.IntN(len(keys))])
		}
		i := 0
		for b.Loop() {
			x := probes[i%len(probes)]
			binary.BigEndian.PutUint64(lo[:], x-min(x, 1000))
			binary.BigEndian.PutUint64(hi[:], x+min(^x, 1000))
			if !f.MayContainRange(lo[:], hi[:]) {
				b.Fatal("false negative")
			}
			i++
		}
	})
}
