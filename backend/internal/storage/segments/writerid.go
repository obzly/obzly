package segments

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/google/uuid"
)

// writerIDFile holds this data directory's writer id. The id belongs to the
// disk, not the process: a restart on the same volume (Docker, a PVC) keeps
// uploading what it wrote before, while a fresh emptyDir gets a new id.
const writerIDFile = ".writer-id"

// localWriter is this process's identity in stream_segments.writer_id and
// which pending rows its uploader takes. Unset (tests, tools): rows get a
// NULL writer_id and the uploader takes every pending row.
type localWriter struct {
	id             string
	uploadsUnowned bool
}

var currentLocalWriter atomic.Pointer[localWriter]

// LoadOrCreateWriterID returns the writer id stored in dataDir, creating it
// on first use.
func LoadOrCreateWriterID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, writerIDFile)

	existing, err := os.ReadFile(path)
	if err == nil {
		if id := strings.TrimSpace(string(existing)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("failed to read writer id %q: %w", path, err)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create data directory %q: %w", dataDir, err)
	}
	id := uuid.NewString()
	// Temp then rename, so a crash never leaves an empty or partial id file.
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, []byte(id+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("failed to write writer id: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return "", fmt.Errorf("failed to finalize writer id: %w", err)
	}
	return id, nil
}

// SetLocalWriter makes every segment row this process creates carry writerID
// and scopes its uploader to those rows. uploadsUnowned also hands it rows
// with no writer_id; set it on background-worker replicas. Call once at
// startup, before any segment is written.
func SetLocalWriter(writerID string, uploadsUnowned bool) {
	currentLocalWriter.Store(&localWriter{id: writerID, uploadsUnowned: uploadsUnowned})
}

// ResetLocalWriter restores the unset state (tests).
func ResetLocalWriter() {
	currentLocalWriter.Store(nil)
}

// LocalWriterID is this process's writer id, or "" when unset.
func LocalWriterID() string {
	if writer := currentLocalWriter.Load(); writer != nil {
		return writer.id
	}
	return ""
}

func stampLocalWriter(record *SegmentRecord) {
	if record.WriterID != nil {
		return
	}
	if id := LocalWriterID(); id != "" {
		record.WriterID = &id
	}
}
