//go:build !unix

package segments

import "io/fs"

// allocatedSize is the file's size where blocks aren't known.
func allocatedSize(info fs.FileInfo) int64 { return info.Size() }
