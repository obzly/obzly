package logs_querier

// MergedRows is how many aggregation rows merges handed back to Go so far.
func (q *LogQuerier) MergedRows() int64 {
	return q.mergedRows.Load()
}
