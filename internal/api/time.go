package api

import "time"

// SQLite CURRENT_TIMESTAMP is UTC even when the server runs in another zone.
func databaseTimeInServerZone(value string) string {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.In(time.Local).Format(time.RFC3339)
		}
	}
	return value
}
