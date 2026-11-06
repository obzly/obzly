//go:build unix

package segments

import (
	"io/fs"
	"syscall"
)

// allocatedSize is the disk a file takes: less than its size for a sparse
// file (a column-only segment, partial.go).
func allocatedSize(info fs.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return min(info.Size(), stat.Blocks*512)
	}
	return info.Size()
}
