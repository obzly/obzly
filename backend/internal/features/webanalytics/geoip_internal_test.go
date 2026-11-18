package webanalytics

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_CountryDB_WhenTheFileIsMissing_ReportsNoGeographyAndLooksNothingUp(t *testing.T) {
	db := newCountryDB(func() string { return filepath.Join(t.TempDir(), "absent.mmdb") }, slog.Default())

	info := db.Info()
	assert.False(t, info.Available)
	assert.Contains(t, info.Error, "not found")
	assert.Empty(t, db.lookup("198.51.100.0/24"), "no country, not \"unknown\"")
}

func Test_CountryDB_WhenTheFileIsNotAnMMDB_ReportsNoGeography(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.mmdb")
	assert.NoError(t, os.WriteFile(path, []byte("not a database"), 0o600))
	db := newCountryDB(func() string { return path }, slog.Default())

	assert.False(t, db.Info().Available)
	assert.Empty(t, db.lookup("203.0.113.7"))
}

func Test_CountryDB_WhenOff_IsNilSafe(t *testing.T) {
	var db *countryDB
	assert.False(t, db.Info().Available)
	assert.Empty(t, db.lookup("192.0.2.1"))
}

func Test_RollupFinish_WithoutACountryDatabase_KeepsEveryOtherList(t *testing.T) {
	db := newCountryDB(func() string { return "" }, slog.Default())
	r := newRollup()
	sh := srcHost{"service\n\nnginx\nweb-1", "shop.example.com"}
	r.totals[bucketKey{time.Unix(0, 0).UTC(), sh.src, sh.host}] = newTotals()
	r.keys[sh] = keySet{}
	r.keys[sh].add(dimPrefix, "198.51.100.0/24", &keyAgg{hits: 3, visitors: newSketch(keyPrecision)})
	r.keys[sh].add(dimIP, "198.51.100.0/24", &keyAgg{hits: 3, extra: "198.51.100.0/24"})
	r.keys[sh].add(dimPage, "/", &keyAgg{hits: 3})

	r.finish(db, true)

	assert.Empty(t, r.keys[sh][dimCountry], "no database, no countries")
	assert.Nil(t, r.keys[sh][dimPrefix], "prefixes are never kept")
	assert.Equal(t, "", r.keys[sh][dimIP]["198.51.100.0/24"].extra, "the prefix isn't stored as the country")
	assert.Equal(t, int64(3), r.keys[sh][dimPage]["/"].hits)
}
