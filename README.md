# diva

[![Go Reference](https://pkg.go.dev/badge/github.com/thevilledev/diva.svg)](https://pkg.go.dev/github.com/thevilledev/diva)

A range filter for Go: a compact, approximate set of byte-string keys that
tells you whether any key lies between two bounds. "No" is always right.
"Maybe" is occasionally wrong. It does for range scans what a Bloom filter does
for point lookups.

It implements Diva, from
[*Diva: Dynamic Range Filter for Var-Length Keys and Queries*](https://www.vldb.org/pvldb/vol18/p3923-eslami.pdf)
by Navid Eslami, Ioana O. Bercea and Niv Dayan (PVLDB 18(11), 2025): keys and
ranges of any length, with inserts and deletes.

Pure Go, no dependencies, Go 1.24 or later.

```sh
go get github.com/thevilledev/diva
```

## Use

```go
// Keys must be in ascending order.
f, err := diva.Build(slices.Values(sortedKeys))

f.MayContain([]byte("banana"))              // point query
f.MayContainRange([]byte("b"), []byte("c")) // any key in ["b", "c"]?

f.Insert([]byte("blueberry"))
f.Delete([]byte("banana"))
```

`diva.New()` starts an empty filter. `WithFingerprintBits` trades memory for
accuracy. The [package documentation](https://pkg.go.dev/github.com/thevilledev/diva)
covers the rest and has runnable examples.

- Keys are ordered as byte strings. Encode integers big-endian.
- Trailing zero bytes are ignored: `"a"` and `"a\x00"` are the same key.
- A key inserted twice must be deleted twice.
- Delete only keys that are in the filter. Deleting an absent key can remove a
  present one.
- Queries can run concurrently with each other, not with `Insert` or `Delete`.
- Filters cannot be saved or loaded yet.

## Numbers

Uniformly random 8-byte keys on an Apple M1 Max with Go 1.27. Accuracy is for
200,000 keys loaded with `Build`; timings are for one million.

| Fingerprint bits | Bits per key | Point FPR | Short-range FPR |
|---:|---:|---:|---:|
| 4  | 8.3  | 9.0%   | 9.1%  |
| 8 (default) | 12.4 | 0.61%  | 0.70% |
| 12 | 16.6 | 0.042% | 0.14% |

| Operation | Time |
|---|---:|
| `Build`, per key | 26 ns |
| `MayContain` | 115 ns |
| `MayContainRange` | 75 to 150 ns |
| `Insert` into a growing filter | 0.6 µs |
| `Insert` plus `Delete` at a steady size | 0.45 µs |

A filter that grows by inserts loses accuracy: the point FPR reaches about 5%
when the default filter is filled entirely from empty. Rebuilding from the keys
restores it.

Reproduce with `go test -bench . -run '^$'` and
`go test -run FalsePositiveRate -v`.

## How it is implemented

The filter keeps every 1024th key in full, as a sample. For the keys between
two neighbouring samples it skips the bits those samples already determine and
stores a short infix of what follows: about ten bits choose a position in that
interval's infix store, and the fingerprint bits are kept there. A query finds
its interval by binary search over the samples, derives infixes from its bounds
the same way, and looks for a stored infix between them.

| File | Contents |
|---|---|
| [diva.go](diva.go) | The filter: samples, build, insert, delete, queries, splitting and merging stores |
| [infix.go](infix.go) | Turning a key into an infix, and re-deriving infixes when samples change |
| [store.go](store.go) | The infix store, a quotient-filter-style table of packed slots |
| [bits.go](bits.go) | Bit operations on word slices |

It departs from the paper in two main ways. The samples are kept in a sorted
slice instead of a trie, and keys outside the sampled range are kept in full in
two small buffers, so a filter with under about a thousand keys is exact.

The authors' C++ reference, [n3slami/Diva](https://github.com/n3slami/Diva),
was read alongside the paper. This package is a new implementation and shares
no code with it.

## License

[MIT](LICENSE)
