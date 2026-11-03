package database

import "testing"

func TestParseSQLiteTimeAcceptsStoredFormats(t *testing.T) {
	for _, raw := range []string{
		"2026-11-03",
		"2026-11-03 12:49:05.69742 +0000 UTC",
		"2026-11-13T07:54:44.4191435Z",
	} {
		if _, ok, err := parseSQLiteTime(raw); err != nil || !ok {
			t.Fatalf("%q: ok=%v err=%v", raw, ok, err)
		}
	}
}
