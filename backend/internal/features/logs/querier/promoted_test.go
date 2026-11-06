package logs_querier_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	logs_core "obzly/internal/features/logs/core"
	logs_ingester "obzly/internal/features/logs/ingester"
	logs_querier "obzly/internal/features/logs/querier"
	projects_models "obzly/internal/features/projects/models"
	projects_services "obzly/internal/features/projects/services"
	users_enums "obzly/internal/features/users/enums"
	users_models "obzly/internal/features/users/models"
	"obzly/internal/storage"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/stretchr/testify/require"
)

// Logs written before and after a field is promoted answer every filter and
// grouping as the fields JSON alone does, and number fields gain range
// filters. A window with only pre-promotion files still works (schema anchor).
func Test_PromotedFields_AnswerLikeTheFieldsJSON_AcrossOldAndNewFiles(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	projectID := uuid.New()
	require.NoError(t, storage.GetDb().Exec("INSERT INTO projects (id, name) VALUES (?, ?)",
		projectID, "promoted "+projectID.String()[:8]).Error)
	ingester, err := logs_ingester.NewLogIngester(t.TempDir(), segments.WithMaxRows(1_000_000))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ingester.Close() })
	store := func(start time.Time, n int) {
		var items []*logs_core.LogItem
		for i := range n {
			fields := map[string]any{"status": fmt.Sprintf("%d", []int{200, 404, 500}[i%3])}
			switch {
			case i%5 == 0: // no latency at all
			case i%7 == 0:
				fields["latency_ms"] = "slow" // not a number
			default:
				fields["latency_ms"] = float64(i * 10)
			}
			if i%4 == 0 {
				delete(fields, "status")
			}
			items = append(items, &logs_core.LogItem{
				ID: uuid.New(), ProjectID: projectID, Timestamp: start.Add(time.Duration(i) * time.Second),
				Level: logs_core.LogLevelInfo, Message: "request", Fields: fields,
			})
		}
		require.NoError(t, ingester.StoreLogsBatch(map[uuid.UUID][]*logs_core.LogItem{projectID: items}))
		_, err := ingester.FlushAll()
		require.NoError(t, err)
	}
	store(now.Add(-2*time.Hour), 60)   // before any promotion
	stripPromotedColumns(t, projectID) // as written before version 3
	querier, err := logs_querier.NewLogQuerier()
	require.NoError(t, err)
	t.Cleanup(func() { _ = querier.Close() })

	queries := map[string]*logs_core.QueryNode{
		"status = 500":       condition("status", logs_core.ConditionOperatorEquals, "500"),
		"status != 500":      condition("status", logs_core.ConditionOperatorNotEquals, "500"),
		"status in 200,404":  condition("status", logs_core.ConditionOperatorIn, []any{"200", "404"}),
		"status not in 200":  condition("status", logs_core.ConditionOperatorNotIn, []any{"200"}),
		"status contains 0":  condition("status", logs_core.ConditionOperatorContains, "0"),
		"status exists":      condition("status", logs_core.ConditionOperatorExists, nil),
		"status not exists":  condition("status", logs_core.ConditionOperatorNotExists, nil),
		"latency = 30":       condition("latency_ms", logs_core.ConditionOperatorEquals, "30"),
		"latency != 30":      condition("latency_ms", logs_core.ConditionOperatorNotEquals, "30"),
		"latency = slow":     condition("latency_ms", logs_core.ConditionOperatorEquals, "slow"),
		"latency in 20,40":   condition("latency_ms", logs_core.ConditionOperatorIn, []any{"20", "40"}),
		"latency exists":     condition("latency_ms", logs_core.ConditionOperatorExists, nil),
		"latency contains 0": condition("latency_ms", logs_core.ConditionOperatorContains, "0"),
	}
	from, to := now.Add(-3*time.Hour), now.Add(time.Hour)
	ids := func(query *logs_core.QueryNode) []string {
		request := queryRequest(query, from, to)
		request.Limit = 1000
		response, err := querier.ExecuteQuery(projectID, request)
		require.NoError(t, err)
		result := make([]string, 0, len(response.Logs))
		for _, log := range response.Logs {
			result = append(result, log.ID)
		}
		sort.Strings(result)
		return result
	}
	groups := func() map[string]int64 {
		request := aggregationRequest(nil, from, to)
		request.GroupField = "status"
		response, err := querier.ExecuteAggregation(context.Background(), projectID, request)
		require.NoError(t, err)
		result := map[string]int64{}
		for _, bucket := range response.Buckets {
			result[bucket.GroupKey] = bucket.Count
		}
		return result
	}
	want := map[string][]string{}
	for name, query := range queries {
		want[name] = ids(query)
	}
	wantGroups := groups()

	admin := &users_models.User{ID: uuid.New(), Role: users_enums.UserRoleAdmin}
	service := projects_services.GetPromotedFieldService()
	_, err = service.PromoteField(projectID, "status", projects_models.PromotedTypeString, admin)
	require.NoError(t, err)
	_, err = service.PromoteField(projectID, "latency_ms", projects_models.PromotedTypeNumber, admin)
	require.NoError(t, err)

	// Promoted, but every file read predates it.
	for name, query := range queries {
		require.Equal(t, want[name], ids(query), "old files only: %s", name)
	}

	store(now.Add(-time.Hour), 60) // after the promotion
	requirePromotedColumnsFilled(t, projectID)
	for name, query := range queries {
		expected := ids(query)
		require.Len(t, expected, 2*len(want[name]), "%s: the new half answers like the old", name)
	}
	// Only the new file: the text field is on every row, so it is read
	// from its column alone; the number field (some "slow") isn't.
	newest := newestSegment(t, projectID)
	require.NotNil(t, newest.Stats)
	require.Equal(t, int64(1), newest.Stats.PromotedFull)
	from = now.Add(-time.Hour - time.Second)
	for name, query := range queries {
		require.Len(t, ids(query), len(want[name]), "new file only: %s", name)
	}
	from = now.Add(-3 * time.Hour)
	gotGroups := groups()
	for key, count := range wantGroups {
		require.Equal(t, 2*count, gotGroups[key], "group %q", key)
	}

	// Ranges on the number field now work, old rows included.
	request := queryRequest(condition("latency_ms", logs_core.ConditionOperatorGreaterOrEqual, "300"), from, to)
	request.Limit = 1000
	response, err := querier.ExecuteQuery(projectID, request)
	require.NoError(t, err)
	expected := 0
	for i := range 60 {
		if i%5 != 0 && i%7 != 0 && i*10 >= 300 {
			expected++
		}
	}
	require.Len(t, response.Logs, 2*expected)
}

// requirePromotedColumnsFilled: the newest segment's rows carry both
// promotions in their mask and the fields' values in the columns (the
// number one only where the value is a number).
func requirePromotedColumnsFilled(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	newest := newestSegment(t, projectID)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var rows, masked, statusMatches, numbers, numberMatches int
	require.NoError(t, db.QueryRow(`SELECT count(*),
		count(*) FILTER (WHERE promoted_mask & 1 <> 0 AND promoted_mask & 256 <> 0),
		count(*) FILTER (WHERE promoted_s1 IS NOT DISTINCT FROM json_extract_string(fields, '$.status')),
		count(*) FILTER (WHERE promoted_n1 IS NOT NULL),
		count(*) FILTER (WHERE promoted_n1 = TRY_CAST(json_extract_string(fields, '$.latency_ms') AS DOUBLE))
		FROM read_parquet(?)`, newest.Path).Scan(&rows, &masked, &statusMatches, &numbers, &numberMatches))
	require.Equal(t, 60, rows)
	require.Equal(t, rows-countSlow(), masked, "both bits, except where latency isn't a number")
	require.Equal(t, rows, statusMatches)
	require.Positive(t, numbers)
	require.Equal(t, numbers, numberMatches)
}

func countSlow() int {
	n := 0
	for i := range 60 {
		if i%5 != 0 && i%7 == 0 {
			n++
		}
	}
	return n
}

// stripPromotedColumns rewrites the project's files without the promoted
// columns, as version 2 wrote them.
func stripPromotedColumns(t *testing.T, projectID uuid.UUID) {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "logs")
	require.NoError(t, err)
	db, err := sql.Open("duckdb", "")
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, record := range records {
		stripped := record.Path + ".v2"
		promoted := []string{"promoted_mask"}
		for _, slot := range projects_models.PromotedSlots {
			promoted = append(promoted, "promoted_"+slot)
		}
		_, err := db.Exec("COPY (SELECT * EXCLUDE ("+strings.Join(promoted, ", ")+") FROM read_parquet(?)) TO '"+
			stripped+"' (FORMAT PARQUET)", record.Path)
		require.NoError(t, err)
		require.NoError(t, os.Rename(stripped, record.Path))
		var columns int
		require.NoError(t, db.QueryRow(
			"SELECT count(*) FROM (DESCRIBE SELECT * FROM read_parquet(?)) WHERE column_name LIKE 'promoted_%'",
			record.Path).Scan(&columns))
		require.Zero(t, columns)
	}
}

func newestSegment(t *testing.T, projectID uuid.UUID) *segments.SegmentRecord {
	t.Helper()
	records, err := (&segments.SegmentRepository{}).ListByProject(projectID, "logs")
	require.NoError(t, err)
	newest := records[0]
	for _, record := range records {
		if record.CreatedAt.After(newest.CreatedAt) {
			newest = record
		}
	}
	return newest
}
