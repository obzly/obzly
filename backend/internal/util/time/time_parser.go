package time_parser

import "time"

// ParseTimestamp converts a timestamp to time.Time in UTC. Strings may be RFC3339(Nano),
// "2006-01-02T15:04:05[Z]" or "2006-01-02 15:04:05"; numbers are Unix seconds or, above
// 1e12, milliseconds. nil, "" and anything unparseable mean now.
func ParseTimestamp(timestamp any) time.Time {
	if timestamp == nil {
		return time.Now().UTC()
	}

	switch v := timestamp.(type) {
	case string:
		if v == "" {
			return time.Now().UTC()
		}

		formats := []string{
			time.RFC3339,           // "2006-01-02T15:04:05Z07:00"
			time.RFC3339Nano,       // "2006-01-02T15:04:05.999999999Z07:00"
			"2006-01-02T15:04:05Z", // ISO with Z suffix
			"2006-01-02T15:04:05",  // ISO without timezone
			"2006-01-02 15:04:05",  // Space-separated format
		}

		for _, format := range formats {
			if t, err := time.Parse(format, v); err == nil {
				return t.UTC()
			}
		}

		return time.Now().UTC()

	case float64:
		// JSON numbers arrive as float64; values above 1e12 are milliseconds.
		if v > 1e12 { // Milliseconds (timestamp > ~2001-09-09)
			return time.Unix(0, int64(v)*int64(time.Millisecond)).UTC()
		} else { // Seconds
			return time.Unix(int64(v), 0).UTC()
		}

	case int64:
		if v > 1e12 { // Milliseconds
			return time.Unix(0, v*int64(time.Millisecond)).UTC()
		} else { // Seconds
			return time.Unix(v, 0).UTC()
		}

	case int:
		return ParseTimestamp(int64(v))

	default:
		return time.Now().UTC()
	}
}
