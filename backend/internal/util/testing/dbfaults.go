package testing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"obzly/internal/storage"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// FailQueriesOn makes every gorm query on table fail with a connection error,
// a database outage for that table, until the returned stop is called or the
// test ends. The callback is process-wide: don't use it in a t.Parallel test.
func FailQueriesOn(t *testing.T, table string) (stop func()) {
	name := "test:fail_queries_on_" + table
	db := storage.GetDb()
	err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == table {
			_ = tx.AddError(errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"))
		}
	})
	require.NoError(t, err)

	var once sync.Once
	stop = func() {
		once.Do(func() { _ = db.Callback().Query().Remove(name) })
	}
	t.Cleanup(stop)
	return stop
}

// HangQueriesOn makes every gorm query on table hang, a database that accepts
// connections but never answers, until the query's context ends (or 30 s, so
// a missing deadline fails the test instead of hanging it). Process-wide like
// FailQueriesOn.
func HangQueriesOn(t *testing.T, table string) (stop func()) {
	name := "test:hang_queries_on_" + table
	db := storage.GetDb()
	err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != table {
			return
		}
		select {
		case <-tx.Statement.Context.Done():
			_ = tx.AddError(tx.Statement.Context.Err())
		case <-time.After(30 * time.Second):
			_ = tx.AddError(errors.New("query hung for 30 s: no deadline on the request path"))
		}
	})
	require.NoError(t, err)

	var once sync.Once
	stop = func() {
		once.Do(func() { _ = db.Callback().Query().Remove(name) })
	}
	t.Cleanup(stop)
	return stop
}
