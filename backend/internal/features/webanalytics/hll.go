package webanalytics

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

// Unique visitors can't be added across hours, so every count of them is a
// HyperLogLog sketch: rollups store the registers and a range merges them.
// DuckDB computes the registers (min of hash >> p per register, see
// visitorRegistersSQL), so no visitor hash ever leaves the scan.
const (
	// totalPrecision: 4096 registers, about 1.6% error, for a source's
	// visitors per hour or bucket.
	totalPrecision = 12
	// keyPrecision: 256 registers, about 6.5% error, per row of a top list
	// (a path's or a country's visitors), where hundreds are kept per hour.
	keyPrecision = 8

	sketchDense  = 'd'
	sketchSparse = 's'
)

var errBadSketch = errors.New("malformed visitor sketch")

// sketch is a dense HyperLogLog: one byte per register holding the
// largest rank (leading zeros + 1) seen.
type sketch struct {
	p    uint8
	regs []uint8
}

func newSketch(p uint8) *sketch {
	return &sketch{p: p, regs: make([]uint8, 1<<p)}
}

// addMinW records register idx having seen w = hash >> p as its smallest
// remainder, the form visitorRegistersSQL returns.
func (s *sketch) addMinW(idx int, w uint64) {
	if idx < 0 || idx >= len(s.regs) {
		return
	}
	rank := uint8(64 - int(s.p) + 1)
	if w != 0 {
		rank = uint8(bits.LeadingZeros64(w) - int(s.p) + 1)
	}
	if rank > s.regs[idx] {
		s.regs[idx] = rank
	}
}

// addRegisters parses DuckDB's "idx:w,idx:w" aggregate into s.
func (s *sketch) addRegisters(text string) {
	for pair := range strings.SplitSeq(text, ",") {
		idxText, wText, ok := strings.Cut(pair, ":")
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(idxText)
		if err != nil {
			continue
		}
		w, err := strconv.ParseUint(wText, 10, 64)
		if err != nil {
			continue
		}
		s.addMinW(idx, w)
	}
}

// merge folds other into s; a sketch of another precision is ignored.
func (s *sketch) merge(other *sketch) {
	if other == nil || other.p != s.p {
		return
	}
	for i, rank := range other.regs {
		if rank > s.regs[i] {
			s.regs[i] = rank
		}
	}
}

// estimate is the number of distinct values added, with linear counting
// for small cardinalities (no large-range correction: the hash is 64-bit).
func (s *sketch) estimate() int64 {
	m := float64(len(s.regs))
	sum := 0.0
	zeros := 0
	for _, rank := range s.regs {
		sum += math.Ldexp(1, -int(rank))
		if rank == 0 {
			zeros++
		}
	}
	if zeros == len(s.regs) {
		return 0
	}
	alpha := 0.7213 / (1 + 1.079/m)
	estimate := alpha * m * m / sum
	if estimate <= 2.5*m && zeros > 0 {
		estimate = m * math.Log(m/float64(zeros))
	}
	return int64(math.Round(estimate))
}

// encode stores s sparse (register index + rank pairs) while that is
// smaller than dense, which it is for the many low-traffic rows.
func (s *sketch) encode() []byte {
	nonZero := 0
	for _, rank := range s.regs {
		if rank != 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		return nil
	}
	if nonZero*3 < len(s.regs) {
		out := make([]byte, 2, 2+nonZero*3)
		out[0], out[1] = sketchSparse, s.p
		for i, rank := range s.regs {
			if rank != 0 {
				out = binary.BigEndian.AppendUint16(out, uint16(i))
				out = append(out, rank)
			}
		}
		return out
	}
	out := make([]byte, 2, 2+len(s.regs))
	out[0], out[1] = sketchDense, s.p
	return append(out, s.regs...)
}

// decodeSketch reads encode's bytes; nil or empty is an empty sketch of p.
func decodeSketch(data []byte, p uint8) (*sketch, error) {
	s := newSketch(p)
	if len(data) == 0 {
		return s, nil
	}
	if len(data) < 2 || data[1] != p {
		return nil, errBadSketch
	}
	switch data[0] {
	case sketchDense:
		if len(data)-2 != len(s.regs) {
			return nil, errBadSketch
		}
		copy(s.regs, data[2:])
	case sketchSparse:
		body := data[2:]
		if len(body)%3 != 0 {
			return nil, errBadSketch
		}
		for i := 0; i < len(body); i += 3 {
			idx := int(binary.BigEndian.Uint16(body[i:]))
			if idx >= len(s.regs) {
				return nil, errBadSketch
			}
			s.regs[idx] = max(s.regs[idx], body[i+2])
		}
	default:
		return nil, errBadSketch
	}
	return s, nil
}
