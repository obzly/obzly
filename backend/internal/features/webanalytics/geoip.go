package webanalytics

import (
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// unknownCountry is the Countries panel's row for addresses the database
// doesn't place (private ranges, new allocations).
const unknownCountry = "??"

// GeoInfo describes the country database in use, for the settings drawer.
type GeoInfo struct {
	Available    bool      `json:"available"`
	DatabaseType string    `json:"databaseType,omitempty"`
	BuildTime    time.Time `json:"buildTime,omitzero"`
	Error        string    `json:"error,omitempty"`
}

// countryDB looks visitor addresses up in a country MMDB (DB-IP Lite, CC BY
// 4.0, bundled in the image; any GeoIP2-Country compatible file works). It
// opens lazily and a missing or unreadable file only means no countries.
type countryDB struct {
	// path is read on first use (the configuration loads lazily).
	path   func() string
	log    *slog.Logger
	once   sync.Once
	reader *maxminddb.Reader
	info   GeoInfo
}

type countryRecord struct {
	Country struct {
		ISOCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
}

func newCountryDB(path func() string, log *slog.Logger) *countryDB {
	return &countryDB{path: path, log: log}
}

// Info opens the database if it hasn't been and reports what it is.
func (d *countryDB) Info() GeoInfo {
	if d == nil {
		return GeoInfo{Error: "country lookup is off"}
	}
	d.open()
	return d.info
}

// lookup is network's ISO country code, unknownCountry when the database
// doesn't place it, and "" when there is no database.
func (d *countryDB) lookup(network string) string {
	if d == nil {
		return ""
	}
	d.open()
	if d.reader == nil {
		return ""
	}
	prefix, err := netip.ParsePrefix(network)
	var addr netip.Addr
	if err == nil {
		addr = prefix.Addr()
	} else if addr, err = netip.ParseAddr(network); err != nil {
		return unknownCountry
	}
	var record countryRecord
	if err := d.reader.Lookup(addr).Decode(&record); err != nil || record.Country.ISOCode == "" {
		return unknownCountry
	}
	return record.Country.ISOCode
}

func (d *countryDB) open() {
	d.once.Do(func() {
		path := d.path()
		if path == "" {
			d.info = GeoInfo{Error: "no country database configured"}
			return
		}
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				d.info = GeoInfo{Error: "country database not found at " + path}
			} else {
				d.info = GeoInfo{Error: "country database unreadable"}
			}
			d.log.Warn("Web analytics: no country database, the Countries panel stays empty", "path", path,
				"error", err)
			return
		}
		reader, err := maxminddb.Open(path)
		if err != nil {
			d.info = GeoInfo{Error: "country database could not be opened"}
			d.log.Warn("Web analytics: country database could not be opened", "path", path, "error", err)
			return
		}
		d.reader = reader
		d.info = GeoInfo{Available: true, DatabaseType: reader.Metadata.DatabaseType,
			BuildTime: reader.Metadata.BuildTime().UTC()}
	})
}
