package segments

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/cespare/xxhash/v2"
)

// Token bloom filters: a filter of each log segment's ID-like tokens, kept
// next to its file (<path>.bloom, <object key>.bloom), so a search for an ID
// skips segments that can't contain it without downloading them.
//
// An ID-like token is a maximal run of 12+ letters and digits holding a
// digit, or a UUID-shaped run of letters, digits and dashes. Such a term
// matches only as a whole token, so skipping a segment without it is exact.

// TokenBloomColumns: the column whose tokens each stream's filters hold.
var TokenBloomColumns = map[string]string{"logs": "message", "ai_logs": "message"}

// BloomSuffix is appended to a segment's path and object key.
const BloomSuffix = ".bloom"

const (
	bloomMagic   = "OBZTB1"
	bloomHashes  = 7
	bloomFPR     = 0.01
	bloomMinBits = 8 * 1024
	bloomHeader  = len(bloomMagic) + 1 + 8 // magic, hash count, bit count

	// Lists a file's distinct ID-like tokens, matching IsIDToken.
	tokenRunsSQL = `SELECT DISTINCT token FROM (
		SELECT unnest(regexp_extract_all(%[1]s, '[A-Za-z0-9]{12,}')) AS token FROM read_parquet(?)
		UNION ALL
		SELECT unnest(regexp_extract_all(%[1]s, '[A-Za-z0-9-]*` + uuidCore + `[A-Za-z0-9-]*')) FROM read_parquet(?)
	) WHERE (regexp_full_match(token, '[A-Za-z0-9]{12,}') AND regexp_matches(token, '[0-9]'))
		OR regexp_full_match(token, '` + uuidCore + `')`
	uuidCore = `[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`
)

var (
	alnumTokenPattern = regexp.MustCompile(`^[A-Za-z0-9]{12,}$`)
	uuidTokenPattern  = regexp.MustCompile(`^` + uuidCore + `$`)
	digitPattern      = regexp.MustCompile(`[0-9]`)
)

// IsIDToken reports whether term is one ID-like token, matched as a whole
// token and looked up in the filters.
func IsIDToken(term string) bool {
	return uuidTokenPattern.MatchString(term) ||
		(alnumTokenPattern.MatchString(term) && digitPattern.MatchString(term))
}

// IsUUIDToken reports whether term is a UUID token (its boundaries exclude
// dashes, unlike an alphanumeric token's).
func IsUUIDToken(term string) bool {
	return uuidTokenPattern.MatchString(term)
}

// BloomPath is the local filter path of a segment file.
func BloomPath(segmentPath string) string {
	return segmentPath + BloomSuffix
}

// TokenBlooms builds filters on its own DuckDB handle: bounded memory, a
// spill directory for the DISTINCT over a large file.
type TokenBlooms struct {
	db       *sql.DB
	spillDir string
}

const tokenBloomMemoryLimit = "512MB"

// NewTokenBlooms opens the handle, spilling under spillRoot.
func NewTokenBlooms(spillRoot string) (*TokenBlooms, error) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, fmt.Errorf("failed to open token filter duckdb: %w", err)
	}
	spillDir, err := ConfigureMemory(db, tokenBloomMemoryLimit, spillRoot)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("SET threads = 2"); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(spillDir)
		return nil, err
	}
	return &TokenBlooms{db: db, spillDir: spillDir}, nil
}

// Build writes the filter of stream's file at path beside it (reusing one
// already there); false when the stream keeps no filters.
func (t *TokenBlooms) Build(ctx context.Context, stream, path string) (bool, error) {
	if _, ok := TokenBloomColumns[stream]; ok && fileExists(BloomPath(path)) {
		return true, nil
	}
	return BuildTokenBloom(ctx, t.db, stream, path)
}

func (t *TokenBlooms) Close() error {
	err := t.db.Close()
	if t.spillDir != "" {
		_ = os.RemoveAll(t.spillDir)
	}
	return err
}

// BuildTokenBloom writes the filter of stream's file at path to
// BloomPath(path), using db (a handle whose memory limit and spill
// directory bound the DISTINCT). False when the stream keeps no filters.
func BuildTokenBloom(ctx context.Context, db *sql.DB, stream, path string) (bool, error) {
	column, ok := TokenBloomColumns[stream]
	if !ok {
		return false, nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	table := "tokens_" + strings.ReplaceAll(filepath.Base(path), ".", "_")
	table = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, table)
	if _, err := conn.ExecContext(
		ctx,
		"CREATE TEMP TABLE "+table+" AS "+fmt.Sprintf(tokenRunsSQL, column),
		path,
		path,
	); err != nil {
		return false, fmt.Errorf("failed to list tokens of %s: %w", path, err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table) }()

	var count int64
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		return false, err
	}
	bloom := newBloom(count)
	rows, err := conn.QueryContext(ctx, "SELECT token FROM "+table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			return false, err
		}
		bloom.add(token)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	target := BloomPath(path)
	temp := target + ".tmp"
	if err := os.WriteFile(temp, bloom.encode(), 0o644); err != nil {
		_ = os.Remove(temp)
		return false, fmt.Errorf("failed to write token filter: %w", err)
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return false, fmt.Errorf("failed to place token filter: %w", err)
	}
	return true, nil
}

// SkipByTokens drops the records whose filter says one of tokens is absent.
// A record without a filter, or whose filter can't be read, is kept: it is
// scanned as before. Filters are read from beside a local file, else from
// cacheDir, else downloaded from store into cacheDir.
func SkipByTokens(
	ctx context.Context, store *ObjectStore, cacheDir string, records []*SegmentRecord, tokens []string,
) []*SegmentRecord {
	if len(tokens) == 0 {
		return records
	}
	kept := make([]*SegmentRecord, len(records))
	var wg sync.WaitGroup
	slots := make(chan struct{}, bloomFetchConcurrency)
	for i, record := range records {
		if record.Stats == nil || !record.Stats.TokenBloom {
			kept[i] = record
			continue
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			if mayContainAll(ctx, store, cacheDir, record, tokens) {
				kept[i] = record
			}
		})
	}
	wg.Wait()
	result := make([]*SegmentRecord, 0, len(records))
	for _, record := range kept {
		if record != nil {
			result = append(result, record)
		}
	}
	return result
}

// FetchTokenBloom puts record's filter into cacheDir (pre-fetch, warm-up).
func FetchTokenBloom(ctx context.Context, store *ObjectStore, cacheDir string, record *SegmentRecord) {
	if record.Stats == nil || !record.Stats.TokenBloom {
		return
	}
	_, _ = localBloom(ctx, store, cacheDir, record)
}

// bloomFetchConcurrency: filters a query checks (and downloads) at once.
const bloomFetchConcurrency = 16

func mayContainAll(
	ctx context.Context, store *ObjectStore, cacheDir string, record *SegmentRecord, tokens []string,
) bool {
	path, err := localBloom(ctx, store, cacheDir, record)
	if err != nil {
		return true
	}
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer func() { _ = file.Close() }()
	for _, token := range tokens {
		present, err := bloomContains(file, token)
		if err != nil {
			return true
		}
		if !present {
			return false
		}
	}
	return true
}

func localBloom(ctx context.Context, store *ObjectStore, cacheDir string, record *SegmentRecord) (string, error) {
	if path := BloomPath(record.Path); fileExists(path) {
		return path, nil
	}
	cached := filepath.Join(cacheDir, record.Stream, record.ProjectID.String(), record.ID.String()+BloomSuffix)
	if fileExists(cached) {
		return cached, nil
	}
	if store == nil || record.ObjectKey == "" {
		return "", errors.New("token filter not available")
	}
	if err := store.Download(ctx, record.ObjectKey+BloomSuffix, cached); err != nil {
		return "", fmt.Errorf("failed to download token filter: %w", err)
	}
	return cached, nil
}

// bloom is a plain bit array with bloomHashes double-hashed positions.
type bloom struct {
	bits   []byte
	nbits  uint64
	hashes uint8
}

func newBloom(tokens int64) *bloom {
	nbits := uint64(math.Ceil(-float64(max(tokens, 1)) * math.Log(bloomFPR) / (math.Ln2 * math.Ln2)))
	nbits = max(nbits, bloomMinBits)
	nbits = (nbits + 7) / 8 * 8
	return &bloom{bits: make([]byte, nbits/8), nbits: nbits, hashes: bloomHashes}
}

func (b *bloom) add(token string) {
	h1, h2 := bloomHashPair(token)
	for i := range uint64(b.hashes) {
		bit := (h1 + i*h2) % b.nbits
		b.bits[bit/8] |= 1 << (bit % 8)
	}
}

func (b *bloom) encode() []byte {
	out := make([]byte, bloomHeader, bloomHeader+len(b.bits))
	copy(out, bloomMagic)
	out[len(bloomMagic)] = b.hashes
	binary.LittleEndian.PutUint64(out[len(bloomMagic)+1:], b.nbits)
	return append(out, b.bits...)
}

// bloomContains checks token against an encoded filter, reading only its
// header and the bytes holding the token's bits.
func bloomContains(file *os.File, token string) (bool, error) {
	header := make([]byte, bloomHeader)
	if _, err := file.ReadAt(header, 0); err != nil {
		return false, err
	}
	if string(header[:len(bloomMagic)]) != bloomMagic {
		return false, errors.New("not a token filter")
	}
	hashes := uint64(header[len(bloomMagic)])
	nbits := binary.LittleEndian.Uint64(header[len(bloomMagic)+1:])
	if nbits == 0 {
		return false, errors.New("empty token filter")
	}
	h1, h2 := bloomHashPair(token)
	one := make([]byte, 1)
	for i := range hashes {
		bit := (h1 + i*h2) % nbits
		if _, err := file.ReadAt(one, int64(bloomHeader)+int64(bit/8)); err != nil {
			return false, err
		}
		if one[0]&(1<<(bit%8)) == 0 {
			return false, nil
		}
	}
	return true, nil
}

func bloomHashPair(token string) (uint64, uint64) {
	h := xxhash.Sum64String(token)
	return h & 0xffffffff, (h >> 32) | 1
}
