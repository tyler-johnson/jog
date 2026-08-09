package cli

import "testing"

func TestNormalizeTime(t *testing.T) {
	cases := map[string]string{
		"30m":        "30.minutes.ago",
		"1h":         "1.hours.ago",
		"2d":         "2.days.ago",
		"1w":         "1.weeks.ago",
		"45s":        "45.seconds.ago",
		"@{1h}":      "@{1.hours.ago}", // shorthand inside bare reflog braces
		"@{2}":       "@{2}",           // reflog ordinal, not a time
		"":           "",
		"yesterday":  "yesterday",  // git's own syntax passes through
		"1.hour.ago": "1.hour.ago", // already dotted
		"c0ffee1":    "c0ffee1",    // snap id
		"1hh":        "1hh",
		"h1":         "h1",
		"@{unclosed": "@{unclosed",
	}
	for in, want := range cases {
		if got := normalizeTime(in); got != want {
			t.Errorf("normalizeTime(%q) = %q, want %q", in, got, want)
		}
	}
}
