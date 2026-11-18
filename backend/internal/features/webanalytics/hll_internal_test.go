package webanalytics

import (
	"fmt"
	"math"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SketchEstimate_ForKnownCardinalities_IsWithinTheExpectedError(t *testing.T) {
	for _, tc := range []struct {
		precision uint8
		distinct  int
		tolerance float64
	}{
		{totalPrecision, 10, 0.01}, {totalPrecision, 1_000, 0.04}, {totalPrecision, 50_000, 0.05},
		{keyPrecision, 40, 0.1}, {keyPrecision, 5_000, 0.2},
	} {
		s := newSketch(tc.precision)
		for i := range tc.distinct {
			addHash(s, xxhash.Sum64String(fmt.Sprintf("visitor-%d", i)))
			addHash(s, xxhash.Sum64String(fmt.Sprintf("visitor-%d", i))) // repeats don't count
		}
		estimate := float64(s.estimate())
		assert.InDelta(t, float64(tc.distinct), estimate, math.Max(1, tc.tolerance*float64(tc.distinct)),
			"p=%d, %d distinct", tc.precision, tc.distinct)
	}
}

func Test_SketchMerge_OfOverlappingHours_CountsEachVisitorOnce(t *testing.T) {
	first, second := newSketch(totalPrecision), newSketch(totalPrecision)
	for i := range 3000 {
		addHash(first, xxhash.Sum64String(fmt.Sprintf("v%d", i)))
	}
	for i := 2000; i < 5000; i++ {
		addHash(second, xxhash.Sum64String(fmt.Sprintf("v%d", i)))
	}
	first.merge(second)
	assert.InDelta(t, 5000, first.estimate(), 5000*0.04, "2000-2999 are in both hours")
}

func Test_SketchEncode_SparseAndDense_RoundTrips(t *testing.T) {
	sparse, dense := newSketch(totalPrecision), newSketch(keyPrecision)
	for i := range 20 {
		addHash(sparse, xxhash.Sum64String(fmt.Sprintf("s%d", i)))
	}
	for i := range 5000 {
		addHash(dense, xxhash.Sum64String(fmt.Sprintf("d%d", i)))
	}
	sparseBytes, denseBytes := sparse.encode(), dense.encode()
	assert.Equal(t, byte(sketchSparse), sparseBytes[0])
	assert.Less(t, len(sparseBytes), 100, "20 visitors take a few bytes, not 4 KB")
	assert.Equal(t, byte(sketchDense), denseBytes[0])

	decodedSparse, err := decodeSketch(sparseBytes, totalPrecision)
	require.NoError(t, err)
	assert.Equal(t, sparse.regs, decodedSparse.regs)
	decodedDense, err := decodeSketch(denseBytes, keyPrecision)
	require.NoError(t, err)
	assert.Equal(t, dense.regs, decodedDense.regs)

	empty, err := decodeSketch(nil, totalPrecision)
	require.NoError(t, err)
	assert.Zero(t, empty.estimate())
	assert.Nil(t, newSketch(totalPrecision).encode())

	_, err = decodeSketch(denseBytes, totalPrecision)
	assert.ErrorIs(t, err, errBadSketch, "another precision")
	_, err = decodeSketch([]byte{'x', totalPrecision}, totalPrecision)
	assert.ErrorIs(t, err, errBadSketch)
}

func Test_SketchAddRegisters_FromDuckDBText_MatchesAddingHashes(t *testing.T) {
	direct, parsed := newSketch(keyPrecision), newSketch(keyPrecision)
	text := ""
	for i := range 50 {
		hash := xxhash.Sum64String(fmt.Sprintf("r%d", i))
		addHash(direct, hash)
		if text != "" {
			text += ","
		}
		text += fmt.Sprintf("%d:%d", hash&((1<<keyPrecision)-1), hash>>keyPrecision)
	}
	parsed.addRegisters(text + ",garbage,7:x")
	assert.Equal(t, direct.regs, parsed.regs)
}

func addHash(s *sketch, hash uint64) {
	s.addMinW(int(hash&((1<<s.p)-1)), hash>>s.p)
}
