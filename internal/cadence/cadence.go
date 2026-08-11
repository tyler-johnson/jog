// Package cadence interprets jog's "how often" settings — jog.updateCheck
// and jog.autoTrim — which share one value language: a bool (true = the
// default cadence, false = off), a number of seconds (3600 = hourly,
// 0 = off), or a git expiry duration ("12.hours", "2.weeks", "never" = off).
package cadence

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// Default is the cadence an unset (or bare-true) setting means, and how
// often a disabled setting is re-read to notice a re-enable.
const Default = 24 * time.Hour

// Min floors a configured cadence: git's expiry parser is lenient enough
// to turn odd values into "now", and a per-command background spawn must
// never be what a typo buys.
const Min = time.Minute

// Read interprets one cadence setting via read, which runs
// `git config [typeFlags…] --get <key>` for the caller's key and scope
// and returns trimmed output. The raw value is inspected before git's
// expiry parser sees it: approxidate happily reads "false" as a date,
// and a bare number as one too — "3600" is now, "7" is the 7th of this
// month — so bools and seconds are peeled off first.
func Read(read func(typeFlags ...string) (string, error)) (time.Duration, bool) {
	raw, err := read()
	if err != nil {
		return Default, true
	}
	val := strings.ToLower(raw)
	switch val {
	case "false", "no", "off", "never":
		return 0, false
	case "true", "yes", "on", "":
		return Default, true
	}
	if secs, err := strconv.ParseUint(val, 10, 63); err == nil {
		if secs == 0 {
			return 0, false
		}
		return max(time.Duration(secs)*time.Second, Min), true
	}
	out, err := read("--type=expiry-date")
	if err != nil {
		return Default, true
	}
	epoch, err := strconv.ParseUint(out, 10, 64)
	switch {
	case err != nil:
		return Default, true
	case epoch == 0:
		// Expiry-speak for "never expire" — never run.
		return 0, false
	case epoch > math.MaxInt64:
		// Expiry-speak for "expire everything" ("now", "all") — the
		// shortest cadence there is.
		return Min, true
	}
	return max(time.Since(time.Unix(int64(epoch), 0)), Min), true
}

// Encode is the state-file encoding of Read's answer: -1 disabled, else
// whole seconds. State files cache the parsed setting so hot paths decide
// staleness from one file read, never a config spawn.
func Encode(iv time.Duration, enabled bool) int64 {
	if !enabled {
		return -1
	}
	return int64(iv / time.Second)
}

// Interval decodes a cached cadence. Disabled (and the zero value) return
// the default: that is how often the config is re-read to notice a
// re-enable.
func Interval(secs int64) time.Duration {
	if secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return Default
}
