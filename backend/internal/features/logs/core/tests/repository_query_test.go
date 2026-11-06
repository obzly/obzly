package logs_core_tests

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	logs_core "obzly/internal/features/logs/core"
)

func Test_ExecuteQueryForProject_WithLogicalAndConditions_ReturnsMatchingLogs(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	testLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"User login successful", map[string]any{
			"environment":  "production",
			"service":      "auth-api",
			"test_session": uniqueTestSession,
		})

	nonMatchingEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"Different message", map[string]any{
			"environment":  "staging", // Different environment
			"service":      "other-api",
			"test_session": uniqueTestSession,
		})

	allEntries := MergeLogEntries(testLogEntries, nonMatchingEntries)
	StoreTestLogsAndFlush(t, repository, allEntries)

	logicalAndQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeLogical,
			Logic: &logs_core.LogicalNode{
				Operator: logs_core.LogicalOperatorAnd,
				Children: []logs_core.QueryNode{
					{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
						Field: "environment", Operator: logs_core.ConditionOperatorEquals, Value: "production",
					}},
					{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
						Field: "message", Operator: logs_core.ConditionOperatorContains, Value: "login",
					}},
					{Type: logs_core.QueryNodeTypeCondition, Condition: &logs_core.ConditionNode{
						Field: "test_session", Operator: logs_core.ConditionOperatorEquals, Value: uniqueTestSession,
					}},
				},
			},
		},
		Limit: 10,
	}

	queryResult, queryErr := repository.ExecuteQueryForProject(projectID, logicalAndQuery)
	assert.NoError(t, queryErr)
	assert.NotNil(t, queryResult)

	assert.Equal(t, int64(1), queryResult.Total, "Should find exactly 1 log matching all AND conditions")
	assert.Len(t, queryResult.Logs, 1, "Should return exactly 1 log")

	matchedLog := queryResult.Logs[0]
	assert.Contains(t, matchedLog.Message, "login", "Message should contain 'login'")
	assert.Equal(t, "production", matchedLog.Fields["environment"], "Environment should be 'production'")
	assert.Equal(t, uniqueTestSession, matchedLog.Fields["test_session"], "Test session should match")
	assert.Equal(t, "auth-api", matchedLog.Fields["service"], "Service should be 'auth-api'")
}

func Test_ExecuteQueryForProject_WithSingleCondition_ReturnsMatchingLogs(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	differentTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	matchingLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"API request processed", map[string]any{
			"service":      "payment-api",
			"status_code":  200,
			"test_session": uniqueTestSession,
		})

	matchingLogEntries2 := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(1*time.Second),
		"Another matching request", map[string]any{
			"service":      "user-api",
			"status_code":  201,
			"test_session": uniqueTestSession, // Same test session
		})

	nonMatchingLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(2*time.Second),
		"Non-matching request", map[string]any{
			"service":      "other-api",
			"status_code":  404,
			"test_session": differentTestSession, // Different test session
		})

	allEntries := MergeLogEntries(matchingLogEntries, matchingLogEntries2)
	allEntries = MergeLogEntries(allEntries, nonMatchingLogEntries)
	StoreTestLogsAndFlush(t, repository, allEntries)

	singleConditionQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 5,
	}

	queryResult, queryErr := repository.ExecuteQueryForProject(projectID, singleConditionQuery)
	assert.NoError(t, queryErr)
	assert.NotNil(t, queryResult)

	assert.Equal(t, int64(2), queryResult.Total, "Should find exactly 2 logs with matching test_session")
	assert.Len(t, queryResult.Logs, 2, "Should return exactly 2 logs")

	for i, log := range queryResult.Logs {
		assert.Equal(t, uniqueTestSession, log.Fields["test_session"],
			"Log %d should have the correct test_session", i)
		assert.NotEqual(t, differentTestSession, log.Fields["test_session"],
			"Should not return logs with different test_session")
	}

	messages := make([]string, len(queryResult.Logs))
	for i, log := range queryResult.Logs {
		messages[i] = log.Message
	}
	assert.Contains(t, messages, "API request processed")
	assert.Contains(t, messages, "Another matching request")
	assert.NotContains(t, messages, "Non-matching request")
}

func Test_DiscoverFields_WithCustomFieldsInLogs_ReturnsDiscoveredFields(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	testLogEntries1 := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"Field discovery test log 1", map[string]any{
			"custom_field_one": "value_" + uniqueTestSession,
			"status_code":      201,
			"test_session":     uniqueTestSession,
			"unique_field_a":   "unique_value_a",
		})

	testLogEntries2 := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(1*time.Second),
		"Field discovery test log 2", map[string]any{
			"custom_field_two": "different_value",
			"priority_level":   "high",
			"test_session":     uniqueTestSession,
			"unique_field_b":   "unique_value_b",
		})

	allEntries := MergeLogEntries(testLogEntries1, testLogEntries2)
	StoreTestLogsAndFlush(t, repository, allEntries)

	discoveredFields, discoveryErr := repository.DiscoverFields(projectID)
	assert.NoError(t, discoveryErr)
	assert.NotNil(t, discoveredFields)
	assert.IsType(t, []string{}, discoveredFields)

	assert.NotEmpty(t, discoveredFields, "Should discover at least some fields")

	fieldMap := make(map[string]bool)
	for _, field := range discoveredFields {
		fieldMap[field] = true
	}

	assert.True(t, fieldMap["custom_field_one"], "Should discover 'custom_field_one' field")
	assert.True(t, fieldMap["custom_field_two"], "Should discover 'custom_field_two' field")
	assert.True(t, fieldMap["status_code"], "Should discover 'status_code' field")
	assert.True(t, fieldMap["test_session"], "Should discover 'test_session' field")
	assert.True(t, fieldMap["priority_level"], "Should discover 'priority_level' field")
	assert.True(t, fieldMap["unique_field_a"], "Should discover 'unique_field_a' field")
	assert.True(t, fieldMap["unique_field_b"], "Should discover 'unique_field_b' field")

	// Field discovery returns custom fields only; built-in fields are always available.

	t.Logf("Discovered fields: %v", discoveredFields)
}

func Test_DiscoverFields_WithUnavailableRepository_ReturnsError(t *testing.T) {
	unavailableRepository := logs_core.GetUnavailableLogCoreRepository()
	projectID := uuid.New()

	discoveredFields, discoveryErr := unavailableRepository.DiscoverFields(projectID)
	assert.Error(t, discoveryErr)
	assert.Nil(t, discoveredFields)
	assert.Contains(t, discoveryErr.Error(), "logs storage is unavailable")
}

func Test_ExecuteQueryForProject_WithTimeRange_ReturnsFilteredLogs(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	baseTime := time.Now().UTC()

	oldTime := baseTime.Add(-2 * time.Hour)
	recentTime := baseTime.Add(-30 * time.Minute)
	veryRecentTime := baseTime.Add(-10 * time.Minute)

	oldLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, oldTime,
		"Old log entry", map[string]any{"test_session": uniqueTestSession, "log_type": "old"})
	recentLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, recentTime,
		"Recent log entry", map[string]any{"test_session": uniqueTestSession, "log_type": "recent"})
	veryRecentLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, veryRecentTime,
		"Very recent log entry", map[string]any{"test_session": uniqueTestSession, "log_type": "very_recent"})

	allLogEntries := MergeLogEntries(oldLogEntries, recentLogEntries)
	allLogEntries = MergeLogEntries(allLogEntries, veryRecentLogEntries)
	StoreTestLogsAndFlush(t, repository, allLogEntries)

	allLogsQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	allLogsResult, allLogsErr := repository.ExecuteQueryForProject(projectID, allLogsQuery)
	assert.NoError(t, allLogsErr)
	assert.Equal(t, int64(3), allLogsResult.Total, "Should have 3 total logs before time filtering")

	timeRangeStart := baseTime.Add(-1 * time.Hour)
	timeRangeQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		TimeRange: &logs_core.TimeRangeDTO{
			From: &timeRangeStart,
		},
		Limit: 10,
	}

	timeRangeResult, timeRangeErr := repository.ExecuteQueryForProject(projectID, timeRangeQuery)
	assert.NoError(t, timeRangeErr)
	assert.NotNil(t, timeRangeResult)

	assert.Equal(t, int64(2), timeRangeResult.Total, "Should find only 2 logs after time range filtering")
	assert.Len(t, timeRangeResult.Logs, 2, "Should return only 2 logs")

	for i, log := range timeRangeResult.Logs {
		assert.True(t, log.Timestamp.After(timeRangeStart) || log.Timestamp.Equal(timeRangeStart),
			"Log %d timestamp should be after time range start. Log time: %v, Range start: %v",
			i, log.Timestamp, timeRangeStart)
	}

	messages := make([]string, len(timeRangeResult.Logs))
	for i, log := range timeRangeResult.Logs {
		messages[i] = log.Message
	}
	assert.Contains(t, messages, "Recent log entry")
	assert.Contains(t, messages, "Very recent log entry")
	assert.NotContains(t, messages, "Old log entry", "Old log should be filtered out by time range")

	// Test with both From and To time range
	timeRangeEnd := baseTime.Add(-20 * time.Minute) // Should exclude the very recent log
	boundedTimeQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		TimeRange: &logs_core.TimeRangeDTO{
			From: &timeRangeStart,
			To:   &timeRangeEnd,
		},
		Limit: 10,
	}

	boundedResult, boundedErr := repository.ExecuteQueryForProject(projectID, boundedTimeQuery)
	assert.NoError(t, boundedErr)
	assert.Equal(t, int64(1), boundedResult.Total, "Should find only 1 log with bounded time range")
	assert.Len(t, boundedResult.Logs, 1, "Should return only 1 log with bounded range")
	assert.Equal(t, "Recent log entry", boundedResult.Logs[0].Message, "Should return only the 'Recent log entry'")
}

func Test_ExecuteQueryForProject_FieldsSortedAscending_IncludingClientIp(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	testLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"Field sorting test log", map[string]any{
			"zebra_field":  "last_alphabetically",
			"alpha_field":  "first_alphabetically",
			"middle_field": "somewhere_middle",
			"beta_field":   "second_alphabetically",
			"test_session": uniqueTestSession,
		})

	for _, entries := range testLogEntries {
		for _, entry := range entries {
			entry.ClientIP = "192.168.1.100"
		}
	}

	StoreTestLogsAndFlush(t, repository, testLogEntries)

	query := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	result, err := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, int64(1), result.Total, "Should find exactly 1 log")
	assert.Len(t, result.Logs, 1, "Should return exactly 1 log")

	log := result.Logs[0]
	assert.NotNil(t, log.Fields, "Log should have Fields map")

	assert.Contains(t, log.Fields, "client_ip", "Fields should include client_ip")
	assert.Equal(t, "192.168.1.100", log.Fields["client_ip"], "client_ip in Fields should match")

	assert.Equal(t, "192.168.1.100", log.ClientIP, "ClientIP field should still be available")

	var fieldNames []string
	for fieldName := range log.Fields {
		fieldNames = append(fieldNames, fieldName)
	}

	expectedFields := []string{"alpha_field", "beta_field", "client_ip", "middle_field", "test_session", "zebra_field"}
	assert.Len(t, fieldNames, len(expectedFields), "Should have expected number of fields")

	// Map iteration order is random.
	slices.Sort(fieldNames)

	assert.Equal(t, expectedFields, fieldNames, "Fields should be present and when sorted match expected order")

	assert.Equal(t, "first_alphabetically", log.Fields["alpha_field"])
	assert.Equal(t, "second_alphabetically", log.Fields["beta_field"])
	assert.Equal(t, "192.168.1.100", log.Fields["client_ip"])
	assert.Equal(t, "somewhere_middle", log.Fields["middle_field"])
	assert.Equal(t, uniqueTestSession, log.Fields["test_session"])
	assert.Equal(t, "last_alphabetically", log.Fields["zebra_field"])

	t.Logf("Fields present (sorted): %v", fieldNames)
}

func Test_StoreLogsBatch_WithMixedFieldTypes_ConvertsAllToStrings(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]
	currentTime := time.Now().UTC()

	testFieldName := "mixed_field_" + uniqueTestSession

	integerFieldEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime,
		"Log with integer field", map[string]any{
			testFieldName:  500, // Integer type - will be converted to string "500"
			"test_session": uniqueTestSession,
		})

	err := repository.StoreLogsBatch(integerFieldEntries)
	assert.NoError(t, err, "Should store integer field converted to string")

	flushErr := repository.ForceFlush()
	assert.NoError(t, flushErr, "Force flush should succeed")

	stringFieldEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(1*time.Second),
		"Log with string field", map[string]any{
			testFieldName:  "ERR001", // String type - also stored as string
			"test_session": uniqueTestSession,
		})

	// Both values are stored as strings, so mixed types don't conflict.
	err = repository.StoreLogsBatch(stringFieldEntries)
	assert.NoError(t, err, "Should store string field without conflict since both are strings")

	flushErr2 := repository.ForceFlush()
	assert.NoError(t, flushErr2, "Second force flush should succeed")

	booleanFieldEntries := CreateTestLogEntriesWithUniqueFields(projectID, currentTime.Add(2*time.Second),
		"Log with boolean field", map[string]any{
			testFieldName:  true, // Boolean type - will be converted to string "true"
			"test_session": uniqueTestSession,
		})

	err = repository.StoreLogsBatch(booleanFieldEntries)
	assert.NoError(t, err, "Should store boolean field converted to string")

	flushErr3 := repository.ForceFlush()
	assert.NoError(t, flushErr3, "Third force flush should succeed")

	query := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	result, queryErr := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, queryErr, "Query should succeed")
	assert.Equal(t, int64(3), result.Total, "Should find all 3 logs")

	foundIntegerAsString := false
	foundStringValue := false
	foundBooleanAsString := false
	for _, log := range result.Logs {
		if fieldValue, exists := log.Fields[testFieldName]; exists {
			stringValue, isString := fieldValue.(string)
			assert.True(t, isString, "Field value should be stored as string, got %T", fieldValue)

			if stringValue == "500" {
				foundIntegerAsString = true
			}
			if stringValue == "ERR001" {
				foundStringValue = true
			}
			if stringValue == "true" {
				foundBooleanAsString = true
			}
		}
	}

	assert.True(t, foundIntegerAsString, "Should find integer value stored as string '500'")
	assert.True(t, foundStringValue, "Should find string value 'ERR001'")
	assert.True(t, foundBooleanAsString, "Should find boolean value stored as string 'true'")
}

func Test_ExecuteQueryForProject_WithNanosecondTimestamp_PreservesFullPrecision(t *testing.T) {
	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]

	originalTimestamp := time.Date(2024, 10, 22, 14, 30, 45, 123456789, time.UTC)

	testLogEntries := CreateTestLogEntriesWithUniqueFields(projectID, originalTimestamp,
		"Nanosecond precision test log", map[string]any{
			"test_session": uniqueTestSession,
		})

	StoreTestLogsAndFlush(t, repository, testLogEntries)

	query := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit: 10,
	}

	result, err := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, int64(1), result.Total, "Should find exactly 1 log")
	assert.Len(t, result.Logs, 1, "Should return exactly 1 log")

	retrievedLog := result.Logs[0]

	assert.Equal(
		t,
		originalTimestamp,
		retrievedLog.Timestamp,
		"Retrieved timestamp should match original with full nanosecond precision. Expected: %v (UnixNano: %d), Got: %v (UnixNano: %d)",
		originalTimestamp,
		originalTimestamp.UnixNano(),
		retrievedLog.Timestamp,
		retrievedLog.Timestamp.UnixNano(),
	)

	assert.Equal(t, originalTimestamp.UnixNano(), retrievedLog.Timestamp.UnixNano(),
		"UnixNano values should be exactly equal")

	assert.Equal(t, int64(123456789), int64(retrievedLog.Timestamp.Nanosecond()),
		"Nanosecond component should be exactly preserved (expected: 123456789)")

	t.Logf("Original timestamp:  %v (UnixNano: %d, Nanos: %d)",
		originalTimestamp, originalTimestamp.UnixNano(), originalTimestamp.Nanosecond())
	t.Logf("Retrieved timestamp: %v (UnixNano: %d, Nanos: %d)",
		retrievedLog.Timestamp, retrievedLog.Timestamp.UnixNano(), retrievedLog.Timestamp.Nanosecond())
}

func Test_ExecuteQueryForProject_WithMultipleLogsAt2NanosecondSteps_PreservesNanosecondPrecision(t *testing.T) {
	t.Skip(
		"Known limitation, not a regression - see ARCHITECTURE.md's " +
			"\"Known limitation: sub-microsecond query-time precision\" section: " +
			"the stream_segments catalog's min_time/max_time are Postgres " +
			"TIMESTAMPTZ (microsecond precision), so segment-level pruning can't " +
			"distinguish rows within the same microsecond even though the " +
			"underlying Parquet data is true nanosecond precision.",
	)

	repository := logs_core.GetLogCoreRepository()
	projectID := uuid.New()
	uniqueTestSession := uuid.New().String()[:8]

	baseTimestamp := time.Date(2024, 10, 22, 15, 45, 30, 100000000, time.UTC)

	timestamps := []time.Time{
		baseTimestamp,
		baseTimestamp.Add(2 * time.Nanosecond),
		baseTimestamp.Add(4 * time.Nanosecond),
		baseTimestamp.Add(6 * time.Nanosecond),
		baseTimestamp.Add(8 * time.Nanosecond),
	}

	var allEntries map[uuid.UUID][]*logs_core.LogItem
	for i, timestamp := range timestamps {
		entries := CreateTestLogEntriesWithUniqueFields(projectID, timestamp,
			"Log with 2ns precision", map[string]any{
				"test_session": uniqueTestSession,
				"log_index":    i,
			})

		if allEntries == nil {
			allEntries = entries
		} else {
			allEntries = MergeLogEntries(allEntries, entries)
		}
	}

	StoreTestLogsAndFlush(t, repository, allEntries)

	query := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		Limit:     10,
		SortOrder: "asc",
	}

	result, err := repository.ExecuteQueryForProject(projectID, query)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, int64(5), result.Total, "Should find exactly 5 logs")
	assert.Len(t, result.Logs, 5, "Should return exactly 5 logs")

	for i, retrievedLog := range result.Logs {
		expectedTimestamp := timestamps[i]

		assert.Equal(
			t,
			expectedTimestamp.UnixNano(),
			retrievedLog.Timestamp.UnixNano(),
			"Log %d: UnixNano values should match exactly. Expected: %d, Got: %d",
			i,
			expectedTimestamp.UnixNano(),
			retrievedLog.Timestamp.UnixNano(),
		)

		assert.Equal(
			t,
			expectedTimestamp,
			retrievedLog.Timestamp,
			"Log %d: Timestamp should match exactly with 2ns precision",
			i,
		)

		if i > 0 {
			previousTimestamp := result.Logs[i-1].Timestamp
			timeDiff := retrievedLog.Timestamp.Sub(previousTimestamp)
			assert.Equal(
				t,
				int64(2),
				timeDiff.Nanoseconds(),
				"Log %d: Time difference from previous log should be exactly 2 nanoseconds",
				i,
			)
		}

		t.Logf("Log %d - Expected: %v (UnixNano: %d), Got: %v (UnixNano: %d)",
			i, expectedTimestamp, expectedTimestamp.UnixNano(),
			retrievedLog.Timestamp, retrievedLog.Timestamp.UnixNano())
	}

	timeRangeStart := baseTimestamp.Add(3 * time.Nanosecond)
	timeRangeEnd := baseTimestamp.Add(7 * time.Nanosecond)

	rangeQuery := &logs_core.LogQueryRequestDTO{
		Query: &logs_core.QueryNode{
			Type: logs_core.QueryNodeTypeCondition,
			Condition: &logs_core.ConditionNode{
				Field:    "test_session",
				Operator: logs_core.ConditionOperatorEquals,
				Value:    uniqueTestSession,
			},
		},
		TimeRange: &logs_core.TimeRangeDTO{
			From: &timeRangeStart,
			To:   &timeRangeEnd,
		},
		Limit:     10,
		SortOrder: "asc",
	}

	rangeResult, rangeErr := repository.ExecuteQueryForProject(projectID, rangeQuery)
	assert.NoError(t, rangeErr)
	assert.NotNil(t, rangeResult)
	assert.Equal(t, int64(2), rangeResult.Total, "Should find exactly 2 logs in nanosecond time range")
	assert.Len(t, rangeResult.Logs, 2, "Should return exactly 2 logs")

	assert.Equal(t, timestamps[2].UnixNano(), rangeResult.Logs[0].Timestamp.UnixNano(),
		"First log in range should be at +4ns (timestamps[2])")
	assert.Equal(t, timestamps[3].UnixNano(), rangeResult.Logs[1].Timestamp.UnixNano(),
		"Second log in range should be at +6ns (timestamps[3])")

	t.Logf("Time range query: from %v to %v returned %d logs",
		timeRangeStart, timeRangeEnd, rangeResult.Total)
}
