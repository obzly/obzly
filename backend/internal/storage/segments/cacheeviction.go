package segments

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// coldCacheLowWaterMarkRatio: eviction frees down to this fraction of
// maxBytes, so a cache sitting at the limit doesn't run a pass on every call.
const coldCacheLowWaterMarkRatio = 0.9

// cachedSegmentFile is one entry EvictColdCache considers for eviction.
type cachedSegmentFile struct {
	path    string
	size    int64
	modTime int64 // unix nanos - os.FileInfo.ModTime().UnixNano()
}

// EvictColdCache enforces maxBytes on cacheDir, the local cache of
// object-storage segments. Every file there is a re-downloadable copy, so
// deleting one is always safe.
//
// LRU by mtime, not atime (often disabled): resolveFile touches mtime on
// download and on every hit. With download time alone, the newest data was
// evicted first, even a file a running query had just resolved.
//
// Safe to run concurrently with itself and with downloads: a deleted file
// re-requested by a query is just a cache miss.
func EvictColdCache(cacheDir string, maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("maxBytes must be positive, got %d", maxBytes)
	}

	var files []cachedSegmentFile
	var totalSize int64

	err := filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // cacheDir (or a file within it) vanished mid-walk - nothing to do
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil // deleted between being listed and stat'd (e.g. raced with another evictor) - skip it
			}
			return err
		}

		// A column-only (sparse) file takes only the disk its fetched
		// columns do (partial.go).
		size := allocatedSize(info)
		files = append(files, cachedSegmentFile{
			path:    path,
			size:    size,
			modTime: info.ModTime().UnixNano(),
		})
		totalSize += size
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil // cold tier configured but nothing downloaded into it yet
		}
		return fmt.Errorf("failed to walk cold cache directory %q: %w", cacheDir, err)
	}

	if totalSize <= maxBytes {
		return nil
	}

	// Oldest first: frees the least recently used files first.
	sort.Slice(files, func(i, j int) bool { return files[i].modTime < files[j].modTime })

	targetSize := int64(float64(maxBytes) * coldCacheLowWaterMarkRatio)
	var lastErr error
	for _, f := range files {
		if totalSize <= targetSize {
			break
		}
		if strings.HasSuffix(f.path, partialSuffix) {
			_ = os.Remove(f.path + partialMeta) // its column list goes with it
		}
		if err := os.Remove(f.path); err != nil {
			if os.IsNotExist(err) {
				totalSize -= f.size // already gone (a concurrent evictor, or a fresh redownload's Rename raced this one) - still counts as freed
				continue
			}
			lastErr = fmt.Errorf("failed to remove cached segment %q: %w", f.path, err)
			continue
		}
		totalSize -= f.size
	}

	return lastErr
}
