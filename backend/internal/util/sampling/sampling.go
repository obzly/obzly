// Package sampling is the one deterministic keep decision behind trace tail
// sampling (in DuckDB, at compaction) and log head sampling (in Go, at
// ingest), so a trace's logs and spans are kept or dropped alike.
package sampling

import (
	"crypto/md5"
	"encoding/binary"
	"math/big"
)

// Buckets: an id falls in one of this many buckets; rate r keeps the
// first r*Buckets.
const Buckets = 1_000_000

// SQLBucket is Bucket in DuckDB for the id column expr.
func SQLBucket(expr string) string {
	return "md5_number(" + expr + ") % " + "1000000"
}

// Bucket is DuckDB's md5_number(id) % Buckets: the MD5 digest read as a
// little-endian 128-bit unsigned integer.
func Bucket(id string) int64 {
	digest := md5.Sum([]byte(id))
	low := binary.LittleEndian.Uint64(digest[:8])
	high := binary.LittleEndian.Uint64(digest[8:])
	value := new(big.Int).Lsh(new(big.Int).SetUint64(high), 64)
	value.Or(value, new(big.Int).SetUint64(low))
	return new(big.Int).Mod(value, big.NewInt(Buckets)).Int64()
}

// Threshold is the bucket bound for rate: keep when Bucket < Threshold.
func Threshold(rate float64) int64 {
	return int64(rate * Buckets)
}

// Keep reports whether id is kept at rate.
func Keep(id string, rate float64) bool {
	return Bucket(id) < Threshold(rate)
}
