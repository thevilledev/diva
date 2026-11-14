// Package diva implements Diva, a range filter: a compact, approximate set of
// byte string keys that can tell whether a range of keys is empty.
//
// A range filter is to range scans what a Bloom filter is to point lookups.
// A storage engine asks it whether any key lies between two bounds before it
// reads from disk. The filter answers "no" only when that is certain and
// "maybe" otherwise, so a "no" saves the read and a wrong "maybe" merely
// costs one.
//
// Diva, by Eslami, Bercea and Dayan (PVLDB 18, 2025), combines properties
// that earlier range filters offered only separately: keys and query ranges
// of any length, inserts and deletes, and a false positive rate that depends
// on the bits spent per key and not on the length of the queried range.
//
// # Use
//
// Build a filter from keys in ascending order with [Build], or start with an
// empty one from [New] and add keys with [Filter.Insert]. Query it with
// [Filter.MayContain] and [Filter.MayContainRange], and remove keys with
// [Filter.Delete].
//
// Keys are ordered as byte strings, so integers must be encoded big-endian.
// Trailing zero bytes do not count: "a" and "a\x00" are the same key.
//
// # How it works
//
// The filter keeps every 1024th key in full. These samples divide the key
// space into intervals that each hold about a thousand keys. For the keys of
// one interval the filter drops the bits that the two surrounding samples
// already determine and keeps a short infix of what follows: roughly ten bits
// that select a position in the interval's infix store, and the configured
// number of fingerprint bits that are stored there. A query finds the interval
// of its bounds among the samples, derives infixes the same way, and checks
// whether a stored infix falls between them.
//
// Because samples sit closer together where keys are dense, infixes cover
// lower-order bits there, and the filter resolves keys at the granularity of
// the data. That is also the limit of its guarantee. The false positive rate
// holds for keys that are spread reasonably smoothly between samples. Keys
// that are inserted later and crowd into a small part of an interval cannot
// be told apart there until a new sample divides it.
//
// # Accuracy
//
// A filter built with [Build] answers a point query for an absent key, or a
// query for a short empty range, with a false positive about once in 2^(n-1)
// tries, where n is the number of fingerprint bits.
//
// Inserting into an existing filter costs accuracy over time. A new sample
// narrows the interval of the keys around it, and their stored infixes, cut
// for the wider interval, then resolve one bit less. The rate grows by about
// its initial value each time the filter doubles in size, and settles at two
// to three times the initial value under steady churn at a constant size.
// Rebuilding from the keys restores it.
package diva
