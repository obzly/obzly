package mcp_server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
)

// Cursor paging. A cursor is opaque to the agent: base64url JSON naming the
// tool, a fingerprint of the call's other arguments and where the next page
// starts - an offset into a list the Site returned whole or pages by offset,
// or a Site's own cursor (log_context). search_logs passes the Site's cursor
// through, as it always has.
//
// A page is shortened to fit the result limit before its cursor is made
// (fitCount), so fitResult never drops items a cursor would skip.

// pageCursor is what a cursor holds.
type pageCursor struct {
	Tool      string `json:"t"`
	Filter    string `json:"f,omitempty"`
	Offset    int    `json:"o,omitempty"`
	Direction string `json:"d,omitempty"`
	Next      string `json:"n,omitempty"`
}

// pageBudget leaves room under maxResultBytes for what wrap adds afterwards.
const pageBudget = maxResultBytes - 1_000

var errCursorMismatch = errors.New("this cursor belongs to another call - repeat that call's arguments exactly " +
	"with it, or drop cursor to start from the first page")

// encodeCursor makes the cursor of the next page of tool called with filter
// (its arguments other than cursor and limit).
func encodeCursor(tool string, filter any, next pageCursor) string {
	next.Tool, next.Filter = tool, fingerprint(filter)
	encoded, _ := json.Marshal(next)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// decodeCursor reads a cursor encodeCursor made for the same tool and filter;
// no cursor is the first page.
func decodeCursor(tool string, filter any, cursor string) (pageCursor, error) {
	if cursor == "" {
		return pageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	var decoded pageCursor
	if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Offset < 0 {
		return pageCursor{}, errors.New("invalid cursor - pass nextCursor exactly as a result returned it")
	}
	if decoded.Tool != tool || decoded.Filter != fingerprint(filter) {
		return pageCursor{}, errCursorMismatch
	}
	return decoded, nil
}

// pageOf is the page of a whole list that starts at the cursor: at most limit
// items, and where it starts.
func pageOf[T any](tool string, filter any, cursor string, items []T, limit int) ([]T, int, error) {
	position, err := decodeCursor(tool, filter, cursor)
	if err != nil {
		return nil, 0, err
	}
	start := min(position.Offset, len(items))
	return items[start:min(start+limit, len(items))], start, nil
}

// pageResult is the shape of a paged list: the page under key (shortened to
// fit), returned, total, offset and, when more follow, nextCursor.
func pageResult[T any](
	tool string, filter any, key string, page []T, offset, total int, extra map[string]any,
) map[string]any {
	build := func(n int) map[string]any {
		result := make(map[string]any, len(extra)+5)
		maps.Copy(result, extra)
		result[key], result["returned"], result["total"], result["offset"] = page[:n], n, total, offset
		return result
	}
	kept := fitCount(len(page), func(n int) any { return build(n) })
	result := build(kept)
	addNextPage(result, tool, filter, key, offset, kept, total)
	if kept < len(page) && result["note"] != nil {
		result["note"] = fmt.Sprintf("%s (this page was shortened from %d to fit the result limit)", result["note"],
			len(page))
	}
	return result
}

// addNextPage adds nextCursor and a note when items follow the consumed ones.
func addNextPage(result map[string]any, tool string, filter any, key string, offset, consumed, total int) {
	// Nothing consumed (a cursor past the end) would point at itself.
	if consumed == 0 || offset+consumed >= total {
		return
	}
	result["nextCursor"] = encodeCursor(tool, filter, pageCursor{Offset: offset + consumed})
	result["note"] = fmt.Sprintf("returned %s %d-%d of %d - call again with cursor=nextCursor and the same "+
		"other arguments for the next page", key, offset+1, offset+consumed, total)
}

// fitCount is the largest n <= total (at least 1) whose build(n) encodes within
// pageBudget.
func fitCount(total int, build func(n int) any) int {
	if total <= 1 || encodedSize(build(total)) <= pageBudget {
		return total
	}
	low, high := 1, total-1
	for low < high {
		mid := (low + high + 1) / 2
		if encodedSize(build(mid)) <= pageBudget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low
}

func encodedSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func fingerprint(filter any) string {
	encoded, _ := json.Marshal(filter)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:6])
}
