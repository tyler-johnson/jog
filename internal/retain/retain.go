// Package retain is jog's retention policy: a pure taper over snapshot
// timestamps, no git anywhere (plan D16). Given "now" and a chain's
// snapshot times, it decides keep or drop; everything about *applying* the
// decision (chain rewrite, reflogs, gc) lives in the trim command.
package retain

import "time"

// Policy is the taper: keep everything younger than KeepAll, one snapshot
// per hour up to KeepHourly, one per day up to KeepDaily, nothing older.
// Within a bucket the newest snapshot survives (restic's convention).
type Policy struct {
	KeepAll    time.Duration
	KeepHourly time.Duration
	KeepDaily  time.Duration
}

// Default matches DESIGN §7: everything ≤ 24 h, hourly ≤ 7 d, daily ≤ 90 d.
var Default = Policy{
	KeepAll:    24 * time.Hour,
	KeepHourly: 7 * 24 * time.Hour,
	KeepDaily:  90 * 24 * time.Hour,
}

// Keep returns one verdict per snapshot time, same order as given. Times
// may arrive in any order; bucketing is by the snapshot's own timestamp
// (hour/day in UTC), so verdicts are deterministic for (now, times) and
// independent of ordering.
//
// Two properties trim relies on (tested):
//   - idempotent at fixed now: running Keep over the survivors keeps all
//     of them;
//   - no resurrection: as now advances, a dropped snapshot never becomes
//     kept again.
func (p Policy) Keep(now time.Time, times []time.Time) []bool {
	// Newest per bucket wins: find it in one pass per tier.
	newestHourly := map[int64]time.Time{}
	newestDaily := map[int64]time.Time{}
	for _, t := range times {
		age := now.Sub(t)
		switch {
		case age <= p.KeepAll:
		case age <= p.KeepHourly:
			b := t.UTC().Truncate(time.Hour).Unix()
			if t.After(newestHourly[b]) {
				newestHourly[b] = t
			}
		case age <= p.KeepDaily:
			b := t.UTC().Truncate(24 * time.Hour).Unix()
			if t.After(newestDaily[b]) {
				newestDaily[b] = t
			}
		}
	}

	keep := make([]bool, len(times))
	for i, t := range times {
		age := now.Sub(t)
		switch {
		case age <= p.KeepAll:
			keep[i] = true
		case age <= p.KeepHourly:
			keep[i] = t.Equal(newestHourly[t.UTC().Truncate(time.Hour).Unix()])
		case age <= p.KeepDaily:
			keep[i] = t.Equal(newestDaily[t.UTC().Truncate(24*time.Hour).Unix()])
		}
	}
	return keep
}
