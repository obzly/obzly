package mcp_server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_EncodeResult_OversizedList_KeepsWholeItemsAndValidJSON(t *testing.T) {
	logs := make([]map[string]any, 100)
	for i := range logs {
		logs[i] = map[string]any{"message": strings.Repeat("x", 1500), "i": i}
	}
	text := encodeResult(map[string]any{"totalMatches": 5000, "returned": 100, "logs": logs})

	assert.LessOrEqual(t, len(text), maxResultBytes)
	var decoded struct {
		Returned  int              `json:"returned"`
		Truncated bool             `json:"truncated"`
		Note      string           `json:"note"`
		Logs      []map[string]any `json:"logs"`
	}
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.True(t, decoded.Truncated)
	assert.Greater(t, decoded.Returned, 10)
	assert.Less(t, decoded.Returned, 100)
	assert.Len(t, decoded.Logs, decoded.Returned)
	assert.EqualValues(t, 0, decoded.Logs[0]["i"], "the newest (first) items are kept")
	assert.Contains(t, decoded.Note, "of 100 logs")
}

func Test_EncodeResult_OversizedOther_IsStillValidJSON(t *testing.T) {
	text := encodeResult(map[string]any{"spans": strings.Repeat("y", maxResultBytes*2)})

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	assert.Equal(t, true, decoded["truncated"])
	assert.LessOrEqual(t, len(text), maxResultBytes+1_000)
}

func Test_EncodeResult_SmallResult_Unchanged(t *testing.T) {
	assert.JSONEq(t, `{"a":1}`, encodeResult(map[string]any{"a": 1}))
}

func Test_SelectFields(t *testing.T) {
	fields := map[string]any{"service": "api", "pod": "p-1", "big": strings.Repeat("z", 1000)}

	all := selectFields(fields, nil)
	assert.Len(t, all, 3)
	assert.Len(t, all["big"], maxFieldChars+len("…"))

	assert.Equal(t, map[string]any{"service": "api"}, selectFields(fields, []string{"service"}))
	assert.Nil(t, selectFields(fields, []string{"-"}))
}
