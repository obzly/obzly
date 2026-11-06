package logs_core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Cursor is a search position: the last row of a page in (timestamp, id)
// order. The next page starts strictly after it, so rows arriving at the
// newest end while a user pages back don't shift pages the way an offset
// does.
type Cursor struct {
	Timestamp time.Time `json:"t"`
	ID        string    `json:"id"`
}

// ErrInvalidCursor: a cursor that is not one this API returned.
var ErrInvalidCursor = errors.New("invalid query cursor")

// Encode is the opaque form returned as NextCursor.
func (c Cursor) Encode() string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor parses a NextCursor value.
func DecodeCursor(encoded string) (Cursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.ID == "" || cursor.Timestamp.IsZero() {
		return Cursor{}, ErrInvalidCursor
	}
	return cursor, nil
}
