package diva_test

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/thevilledev/diva"
)

// Build a filter from sorted keys and ask whether ranges may hold a key.
func Example() {
	keys := [][]byte{
		[]byte("apple"),
		[]byte("banana"),
		[]byte("cherry"),
	}
	f, err := diva.Build(slices.Values(keys))
	if err != nil {
		panic(err)
	}

	fmt.Println(f.MayContain([]byte("banana")))
	fmt.Println(f.MayContainRange([]byte("b"), []byte("c")))
	fmt.Println(f.MayContainRange([]byte("d"), []byte("z")))
	// Output:
	// true
	// true
	// false
}

// A filter can also start empty and follow a changing set of keys.
func ExampleFilter_Insert() {
	f, err := diva.New(diva.WithFingerprintBits(10))
	if err != nil {
		panic(err)
	}

	// Integer keys must be big-endian for their byte order to match their
	// numeric order.
	key := func(id uint64) []byte { return binary.BigEndian.AppendUint64(nil, id) }

	for id := uint64(1000); id < 2000; id += 10 {
		f.Insert(key(id))
	}
	fmt.Println(f.Len(), f.MayContainRange(key(1495), key(1505)))

	f.Delete(key(1500))
	fmt.Println(f.Len(), f.MayContainRange(key(1495), key(1505)))
	// Output:
	// 100 true
	// 99 false
}
